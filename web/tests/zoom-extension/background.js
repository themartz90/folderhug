/* global chrome */
// Playwright calls the tabs API from this worker in a temporary browser profile.
chrome.runtime.onInstalled.addListener(() => {
  console.debug('Browser zoom test extension ready.');
});
