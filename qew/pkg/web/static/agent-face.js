(function (root, factory) {
  if (typeof module === 'object' && module.exports) {
    module.exports = factory();
  } else {
    root.jamesAgentFace = factory();
  }
})(typeof window !== 'undefined' ? window : globalThis, function () {
  function render(sessionID) {
    if (!sessionID) return '';
    let state = 2166136261;
    for (const character of sessionID) {
      state = Math.imul(state ^ character.codePointAt(0), 16777619) >>> 0;
    }
    const next = limit => {
      state = (Math.imul(state, 1664525) + 1013904223) >>> 0;
      return (state >>> 16) % limit;
    };
    const accents = ['var(--cp-accent)', 'var(--cp-success)', 'var(--cp-warning)', 'var(--cp-link)'];
    const accent = accents[next(accents.length)];
    const headInset = 4 + next(3) * 2;
    const eyeY = 12 + next(3) * 2;
    const eyeHeight = 2 + next(2) * 2;
    const mouthWidth = 4 + next(4) * 2;
    const hair = next(4);
    const accessory = next(3);
    const rect = (x, y, width, height, fill) =>
      `<rect x="${x}" y="${y}" width="${width}" height="${height}" fill="${fill}"/>`;
    let pixels = rect(0, 0, 32, 32, 'var(--cp-bg)');
    pixels += rect(4, 28, 24, 4, accent);
    pixels += rect(12, 24, 8, 6, 'var(--cp-text-muted)');
    pixels += rect(headInset, 6, 32 - headInset * 2, 20, 'var(--cp-text)');
    pixels += rect(headInset + 2, 4, 28 - headInset * 2, 4, accent);
    if (hair === 0) pixels += rect(headInset, 8, 4, 8, accent);
    if (hair === 1) pixels += rect(16, 6, 16 - headInset, 4, accent);
    if (hair === 2) pixels += rect(14, 0, 4, 6, accent);
    pixels += rect(10, eyeY, 4, eyeHeight, 'var(--cp-bg)');
    pixels += rect(18, eyeY, 4, eyeHeight, 'var(--cp-bg)');
    pixels += rect(16 - mouthWidth / 2, 22, mouthWidth, 2, 'var(--cp-bg)');
    if (accessory === 0) {
      pixels += rect(8, eyeY - 2, 16, 2, accent);
      pixels += rect(14, eyeY, 4, 2, accent);
    } else if (accessory === 1) {
      pixels += rect(headInset - 2, 12, 2, 8, accent);
      pixels += rect(32 - headInset, 12, 2, 8, accent);
    }
    return `<svg class="agent-face" xmlns="http://www.w3.org/2000/svg" width="32" height="32" viewBox="0 0 32 32" shape-rendering="crispEdges" aria-hidden="true" focusable="false">${pixels}</svg>`;
  }

  function renderPhoto(data) {
    if (!data || data.length > 100000 || !/^data:image\/png;base64,[A-Za-z0-9+/]+={0,2}$/.test(data)) return '';
    return `<img class="user-face" src="${data}" width="32" height="32" alt="">`;
  }

  async function readPhoto(file) {
    if (!['image/png', 'image/jpeg', 'image/webp'].includes(file.type) || file.size > 5 * 1024 * 1024) {
      throw new Error('Choose a PNG, JPEG or WebP image up to 5 MiB.');
    }
    let bitmap;
    try {
      bitmap = await createImageBitmap(file);
    } catch {
      throw new Error('Could not read the selected image.');
    }
    try {
      const canvas = document.createElement('canvas');
      canvas.width = canvas.height = 128;
      const size = Math.min(bitmap.width, bitmap.height);
      canvas.getContext('2d').drawImage(bitmap, (bitmap.width - size) / 2, (bitmap.height - size) / 2,
        size, size, 0, 0, canvas.width, canvas.height);
      return canvas.toDataURL('image/png');
    } finally {
      bitmap.close();
    }
  }

  return Object.freeze({ render, renderPhoto, readPhoto });
});
