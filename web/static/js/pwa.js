/* Registers the pass-through service worker (web/pwa.go) so the browser
 * offers to install Go Eat - which puts it in the phone's Share sheet. */
if ('serviceWorker' in navigator && window.isSecureContext) {
    window.addEventListener('load', function () {
        navigator.serviceWorker.register('/sw.js').catch(function () { /* not installable here; nothing to do */ });
    });
}
