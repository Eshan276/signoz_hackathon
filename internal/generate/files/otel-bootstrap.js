// Zero-touch OpenTelemetry bootstrap for Node services.
//
// Loaded via NODE_OPTIONS="--require /otel/otel-bootstrap.js", which runs before
// application code. Unlike Python, Node needs no code changes at all — env vars
// plus this file are sufficient.
//
// Like the Python bootstrap, every failure here must be non-fatal: instrumentation
// problems must never take down the user's app.

const PREFIX = '[signoz-init]';

function log(msg) {
  process.stderr.write(`${PREFIX} ${msg}\n`);
}

(function bootstrap() {
  if (process.env.SIGNOZ_INIT_DISABLE) return;

  const service = process.env.OTEL_SERVICE_NAME || 'unknown-service';
  const endpoint =
    process.env.OTEL_EXPORTER_OTLP_ENDPOINT || 'http://localhost:4318';

  // This file is mounted outside the app directory (e.g. /otel), so a bare
  // require() resolves against /otel/node_modules and finds nothing. Resolve
  // explicitly from the app's working directory instead.
  const { createRequire } = require('module');
  const path = require('path');
  const appRequire = createRequire(path.join(process.cwd(), 'noop.js'));

  const load = (name) => {
    try {
      return appRequire(name);
    } catch (_) {
      try {
        return require(name); // fall back to normal resolution
      } catch (err) {
        return { __error: err };
      }
    }
  };

  const sdkNode = load('@opentelemetry/sdk-node');
  const autoInst = load('@opentelemetry/auto-instrumentations-node');
  const otlpProto = load('@opentelemetry/exporter-trace-otlp-proto');

  const failed = [sdkNode, autoInst, otlpProto].find((m) => m && m.__error);
  if (failed) {
    log(
      `OpenTelemetry packages not resolvable from ${process.cwd()} ` +
        `(${failed.__error.message}); skipping instrumentation.\n` +
        `${PREFIX} re-run \`signoz init\` to have them added, or install ` +
        `@opentelemetry/sdk-node manually.`
    );
    return;
  }

  const { NodeSDK } = sdkNode;
  const { getNodeAutoInstrumentations } = autoInst;
  const { OTLPTraceExporter } = otlpProto;

  try {
    const sdk = new NodeSDK({
      serviceName: service,
      traceExporter: new OTLPTraceExporter({
        url: `${endpoint.replace(/\/$/, '')}/v1/traces`,
      }),
      instrumentations: [
        getNodeAutoInstrumentations({
          // Noisy and rarely useful; filesystem spans swamp real work.
          '@opentelemetry/instrumentation-fs': { enabled: false },
        }),
      ],
    });

    sdk.start();
    log(`instrumented '${service}' -> ${endpoint}`);

    const shutdown = () => {
      sdk.shutdown().catch(() => {}).finally(() => process.exit(0));
    };
    process.on('SIGTERM', shutdown);
    process.on('SIGINT', shutdown);
  } catch (err) {
    log(`SDK start failed (${err.message}); app continues uninstrumented`);
  }
})();
