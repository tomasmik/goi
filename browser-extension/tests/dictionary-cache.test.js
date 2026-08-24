const test = require("node:test");
const assert = require("node:assert/strict");

const dictionaryCache = require("../background/dictionary-cache.js");

test("shares pending dictionary lookups and reuses completed results", async function () {
  let loads = 0;
  let finish;
  const cache = dictionaryCache.create({ ttlMs: 1000, limit: 10 });
  const load = async function () {
    loads += 1;
    await new Promise(function (resolve) { finish = resolve; });
    return "result";
  };

  const first = cache.lookup("読む", load);
  const concurrent = cache.lookup("読む", load);
  await new Promise(setImmediate);
  assert.equal(loads, 1);

  finish();
  assert.deepEqual(await Promise.all([first, concurrent]), ["result", "result"]);
  assert.equal(await cache.lookup("読む", load), "result");
  assert.equal(loads, 1);
});

test("expires old entries and evicts the least recently used entry", async function () {
  let clock = 0;
  let loads = 0;
  const cache = dictionaryCache.create({ ttlMs: 10, limit: 2, now: function () { return clock; } });
  const load = async function () {
    loads += 1;
    return loads;
  };

  assert.equal(await cache.lookup("a", load), 1);
  assert.equal(await cache.lookup("b", load), 2);
  assert.equal(await cache.lookup("a", load), 1);
  assert.equal(await cache.lookup("c", load), 3);
  assert.equal(await cache.lookup("b", load), 4);

  clock = 20;
  assert.equal(await cache.lookup("a", load), 5);
});

test("clear prevents an in-flight result from repopulating the cache", async function () {
  let loads = 0;
  let finish;
  const cache = dictionaryCache.create({ ttlMs: 1000, limit: 10 });
  const load = async function () {
    loads += 1;
    if (loads === 1) {
      await new Promise(function (resolve) { finish = resolve; });
    }
    return loads;
  };

  const first = cache.lookup("読む", load);
  await new Promise(setImmediate);
  cache.clear();
  finish();
  await first;

  assert.equal(await cache.lookup("読む", load), 2);
});

test("can skip storing a result when the connection changes", async function () {
  let loads = 0;
  const cache = dictionaryCache.create({ ttlMs: 1000, limit: 10 });
  const load = async function () {
    loads += 1;
    return loads;
  };

  assert.equal(await cache.lookup("読む", load, function () { return false; }), 1);
  assert.equal(await cache.lookup("読む", load), 2);
});
