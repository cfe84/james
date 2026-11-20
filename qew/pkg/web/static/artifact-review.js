(() => {
  const get = id => document.getElementById(id);
  const review = get('review');
  const frame = get('review-frame');
  const toggle = get('review-toggle');
  const add = get('add-comment');
  const submit = get('submit-review');
  const form = get('review-form');
  const general = get('review-general');
  const list = get('review-comments');
  const status = get('review-status');
  const maxBytes = Number(review.dataset.maxBytes);
  const maxComments = Number(review.dataset.maxComments);
  const encoder = new TextEncoder();
  const comments = [];
  let sha256 = '';
  let documentReady = false;
  let selection = '';
  let selectedRange;
  let highlights;
  let operationID;
  let sending = false;

  function valid(text) {
    return text.trim().length > 0 && encoder.encode(text).length <= maxBytes;
  }

  function updateButtons() {
    add.disabled = sending || !documentReady || !valid(selection) || comments.length >= maxComments;
    const overall = general.value.trim();
    submit.disabled = sending || !documentReady || (!comments.length && !overall) ||
      (overall && !valid(overall)) || comments.some(c => !valid(c.input.value));
  }

  function commentChanged(event) {
    operationID = undefined;
    const input = event.target;
    input.setCustomValidity(encoder.encode(input.value.trim()).length > maxBytes ? `Comment exceeds ${maxBytes} UTF-8 bytes.` : '');
    updateButtons();
  }
  general.addEventListener('input', commentChanged);

  toggle.addEventListener('click', () => {
    const opening = review.hidden;
    review.hidden = !opening;
    get('preview').hidden = opening;
    toggle.textContent = opening ? 'Back to preview' : 'Review';
    toggle.setAttribute('aria-expanded', opening);
    if (opening && !frame.hasAttribute('src')) {
      status.textContent = 'Loading document for review…';
      frame.src = `${review.dataset.base}/preview?review=1`;
    }
  });

  frame.addEventListener('load', () => {
    const doc = frame.contentDocument;
    const source = doc?.getElementById('review-document');
    documentReady = false;
    selection = '';
    selectedRange = undefined;
    if (!source) {
      if (frame.hasAttribute('src')) status.textContent = 'Could not load the document for review. Reload to try again.';
      updateButtons();
      return;
    }
    if (comments.length || (general.value.trim() && sha256 && sha256 !== source.dataset.sha)) {
      status.textContent = 'The review document changed. Copy your comments before reloading.';
      updateButtons();
      return;
    }
    sha256 = source.dataset.sha;
    documentReady = true;
    const view = doc.defaultView;
    highlights = view.CSS?.highlights && view.Highlight && new view.Highlight();
    if (highlights) view.CSS.highlights.set('artifact-comment', highlights);
    status.textContent = '';
    doc.addEventListener('selectionchange', () => {
      const selected = doc.getSelection();
      const range = selected?.rangeCount ? selected.getRangeAt(0) : null;
      const quote = range && source.contains(range.commonAncestorContainer) ? selected.toString().trim() : '';
      if (quote) {
        selection = quote;
        selectedRange = range.cloneRange();
      } else if (document.activeElement === frame) {
        selection = '';
        selectedRange = undefined;
      }
      updateButtons();
    });
    updateButtons();
  });

  add.addEventListener('click', () => {
    if (add.disabled) return;
    const row = document.createElement('li');
    const quote = document.createElement('blockquote');
    quote.textContent = selection;
    const input = document.createElement('textarea');
    input.rows = 3;
    input.maxLength = maxBytes;
    input.setAttribute('aria-label', 'Comment on selected quote');
    const remove = document.createElement('button');
    remove.type = 'button';
    remove.textContent = 'Remove comment';
    const item = { quote: selection, input, range: selectedRange };
    input.addEventListener('input', commentChanged);
    remove.addEventListener('click', () => {
      comments.splice(comments.indexOf(item), 1);
      highlights?.delete(item.range);
      row.remove();
      operationID = undefined;
      updateButtons();
    });
    comments.push(item);
    highlights?.add(item.range);
    operationID = undefined;
    selection = '';
    selectedRange = undefined;
    row.append(quote, input, remove);
    list.append(row);
    input.focus();
    updateButtons();
  });

  form.addEventListener('submit', async event => {
    event.preventDefault();
    if (submit.disabled) return;
    sending = true;
    const fields = form.querySelectorAll('textarea, button[type="button"]');
    fields.forEach(field => { field.disabled = true; });
    updateButtons();
    operationID ??= window.crypto?.randomUUID?.() || `qew-${Date.now().toString(36)}-${Math.random().toString(36).slice(2)}`;
    status.textContent = 'Sending review…';
    try {
      const response = await fetch(`${review.dataset.base}/review`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'QewClient' },
        redirect: 'error',
        body: JSON.stringify({
          sha256,
          operation_id: operationID,
          general_comment: general.value.trim(),
          comments: comments.map(c => ({ quote: c.quote, comment: c.input.value.trim() })),
        }),
      });
      if (response.status !== 204) throw new Error(await response.text() || `HTTP ${response.status}`);
      list.replaceChildren();
      comments.length = 0;
      highlights?.clear();
      general.value = '';
      operationID = undefined;
      status.textContent = 'Review sent to the agent.';
    } catch (error) {
      status.textContent = `Review not confirmed: ${error.message}`;
    } finally {
      sending = false;
      fields.forEach(field => { field.disabled = false; });
      updateButtons();
    }
  });
})();
