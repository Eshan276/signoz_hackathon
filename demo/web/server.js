// Gateway in front of the RAG API.
//
// Its only job is to be a second service, so traces have to cross a process
// boundary. Single-process instrumentation is easy; distributed context
// propagation is the thing worth proving.
//
// No OpenTelemetry code here — `signoz init` adds it.

const express = require('express');
const axios = require('axios');

const app = express();
app.use(express.json());

const API_URL = process.env.API_URL || 'http://api:8000';
const PORT = process.env.PORT || 3000;

app.get('/health', (_req, res) => res.json({ status: 'ok' }));

app.post('/ask', async (req, res) => {
  const q = req.body?.q;
  if (!q) {
    return res.status(400).json({ error: 'body must include "q"' });
  }
  // Forward the session id so cost/latency/groundedness roll up per conversation.
  const session_id = req.body?.session_id || 'anonymous';

  try {
    const { data } = await axios.post(
      `${API_URL}/ask`, { q, session_id }, { timeout: 30000 });
    res.json({ ...data, served_by: 'web-gateway' });
  } catch (err) {
    const status = err.response?.status || 502;
    res.status(status).json({
      error: 'upstream request failed',
      detail: err.response?.data || err.message,
    });
  }
});

app.listen(PORT, () => {
  console.log(`web gateway listening on ${PORT}, upstream ${API_URL}`);
});
