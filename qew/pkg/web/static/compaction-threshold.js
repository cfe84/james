(function (root, factory) {
  if (typeof module === 'object' && module.exports) {
    module.exports = factory();
  } else {
    root.jamesCompactionThreshold = factory();
  }
})(typeof window !== 'undefined' ? window : globalThis, function () {
  const DEFAULT = 150000;
  const LONG_CONTEXT = 800000;
  const MIN = 10000;
  const MAX = 900000;

  function defaultForContext(contextTier) {
    return contextTier === 'long_context' ? LONG_CONTEXT : DEFAULT;
  }

  function effective(value, contextTier) {
    return value === undefined || value === null || value === 0
      ? defaultForContext(contextTier)
      : value;
  }

  function validate(value) {
    if (!/^\d+$/.test(value)) {
      return `Compaction threshold must be an integer from ${MIN} to ${MAX} tokens`;
    }
    const parsed = Number(value);
    if (!Number.isSafeInteger(parsed) || parsed < MIN || parsed > MAX) {
      return `Compaction threshold must be between ${MIN} and ${MAX} tokens`;
    }
    return '';
  }

  return Object.freeze({
    DEFAULT,
    LONG_CONTEXT,
    MIN,
    MAX,
    defaultForContext,
    effective,
    validate,
  });
});
