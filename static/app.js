document.addEventListener('DOMContentLoaded', () => {
  const btn = document.getElementById('run-review-btn');
  const statusEl = document.getElementById('review-status');
  const resultEl = document.getElementById('review-result');
  const jobIdEl = document.getElementById('job-id');

  if (!btn) return;

  function escapeHtml(s) {
    return String(s ?? '')
      .replaceAll('&', '&amp;')
      .replaceAll('<', '&lt;')
      .replaceAll('>', '&gt;')
      .replaceAll('"', '&quot;')
      .replaceAll("'", '&#39;');
  }

  function renderResult(result) {
    const findings = Array.isArray(result.findings)
      ? result.findings
      : [];
    const suggestions = Array.isArray(result.suggestions)
      ? result.suggestions
      : [];

    const findingsHtml = findings.length
      ? findings
          .map(
            (f) => `
        <div class="finding sev-${escapeHtml(f.severity)}">
          <div>
            <span class="pill">${escapeHtml(f.severity)}</span>
            <span class="pill">${escapeHtml(f.confidence)}</span>
            <strong>${escapeHtml(f.path)}</strong>
          </div>
          <div><strong>${escapeHtml(f.title)}</strong></div>
          <div>${escapeHtml(f.comment)}</div>
        </div>
      `,
          )
          .join('')
      : `<div>Существенных проблем не найдено.</div>`;

    const suggestionsHtml = suggestions.length
      ? `<ul>${suggestions.map((s) => `<li>${escapeHtml(s)}</li>`).join('')}</ul>`
      : `<div>Нет.</div>`;

    return `
      <h3>Summary</h3>
      <p>${escapeHtml(result.summary || '')}</p>

      <h3>Findings</h3>
      ${findingsHtml}

      <h3>Suggestions</h3>
      ${suggestionsHtml}
    `;
  }

  async function pollJob(jobId) {
    const resp = await fetch(`/jobs/${encodeURIComponent(jobId)}`);
    const data = await resp.json();

    if (!resp.ok) {
      throw new Error(data.error || 'job status request failed');
    }

    statusEl.textContent = data.status;

    if (data.status === 'done') {
      resultEl.innerHTML = renderResult(data.result || {});
      btn.disabled = false;
      return true;
    }

    if (data.status === 'error') {
      resultEl.innerHTML = `<pre>${escapeHtml(data.error || 'unknown error')}</pre>`;
      btn.disabled = false;
      return true;
    }

    return false;
  }

  btn.addEventListener('click', async () => {
    const prId = btn.dataset.prId;
    btn.disabled = true;
    statusEl.textContent = 'queued';
    resultEl.textContent = 'Запущен AI review...';
    jobIdEl.textContent = '';

    try {
      const resp = await fetch(
        `/pr/${encodeURIComponent(prId)}/review`,
        {
          method: 'POST',
        },
      );

      const data = await resp.json();
      if (!resp.ok) {
        throw new Error(data.error || 'start review failed');
      }

      const jobId = data.job_id;
      jobIdEl.textContent = `Job: ${jobId}`;

      const timer = setInterval(async () => {
        try {
          const finished = await pollJob(jobId);
          if (finished) {
            clearInterval(timer);
          }
        } catch (err) {
          clearInterval(timer);
          btn.disabled = false;
          statusEl.textContent = 'error';
          resultEl.innerHTML = `<pre>${escapeHtml(err.message || String(err))}</pre>`;
        }
      }, 1500);
    } catch (err) {
      btn.disabled = false;
      statusEl.textContent = 'error';
      resultEl.innerHTML = `<pre>${escapeHtml(err.message || String(err))}</pre>`;
    }
  });
});
