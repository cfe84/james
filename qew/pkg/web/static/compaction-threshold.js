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
  const DISPLAY_SCALE = 1000;

  function defaultForContext(contextTier) {
    return contextTier === 'long_context' ? LONG_CONTEXT : DEFAULT;
  }

  function effective(value, contextTier) {
    return value === undefined || value === null || value === 0
      ? defaultForContext(contextTier)
      : value;
  }

  function toDisplay(tokens) {
    return tokens / DISPLAY_SCALE;
  }

  function defaultDisplayForContext(contextTier) {
    return toDisplay(defaultForContext(contextTier));
  }

  function effectiveDisplay(value, contextTier) {
    return toDisplay(effective(value, contextTier));
  }

  function validateDisplay(value) {
    const min = toDisplay(MIN);
    const max = toDisplay(MAX);
    if (!/^\d+(?:\.\d{1,3})?$/.test(value)) {
      return `Compaction threshold must be a number from ${min} to ${max} thousand tokens with at most three decimal places`;
    }
    const parsed = Number(value);
    const tokens = parsed * DISPLAY_SCALE;
    if (!Number.isSafeInteger(tokens) || parsed < min || parsed > max) {
      return `Compaction threshold must be between ${min} and ${max} thousand tokens`;
    }
    return '';
  }

  function toTokens(value) {
    return Math.round(Number(value) * DISPLAY_SCALE);
  }

  return Object.freeze({
    DEFAULT,
    LONG_CONTEXT,
    MIN,
    MAX,
    DISPLAY_SCALE,
    defaultForContext,
    effective,
    toDisplay,
    defaultDisplayForContext,
    effectiveDisplay,
    validateDisplay,
    toTokens,
  });
});
