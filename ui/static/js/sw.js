// Krabber's service worker: it makes the site installable and shows a
// "you're offline" page when a page can't load. It caches nothing else, so
// no signed-in page is ever kept on the device.
var OFFLINE = "/offline";
var CACHE = "krabber-offline-v1";

self.addEventListener("install", function (event) {
  event.waitUntil(caches.open(CACHE).then(function (cache) { return cache.add(OFFLINE); }));
  self.skipWaiting();
});

self.addEventListener("activate", function (event) {
  event.waitUntil(caches.keys().then(function (keys) {
    return Promise.all(keys.filter(function (k) { return k !== CACHE; }).map(function (k) { return caches.delete(k); }));
  }).then(function () { return self.clients.claim(); }));
});

self.addEventListener("fetch", function (event) {
  if (event.request.mode !== "navigate") return;
  event.respondWith(fetch(event.request).catch(function () {
    return caches.match(OFFLINE);
  }));
});
