import test from "node:test";
import assert from "node:assert/strict";
import { normalizePolicyList, policyValue, serializePolicy, setFullscreenValue, setPolicyValue, validatePolicy, policyFields } from "../src/lib/exam-policy.ts";

test("opening and saving an empty policy does not invent overrides or a source tunnel host", () => {
  assert.deepEqual(serializePolicy({}), {});
});

test("visual edits preserve unknown root and nested fields without mutating the source", () => {
  const before = { extension: { version: 2, rules: ["x"] }, browser: { custom_flag: true, lock_fullscreen: false }, navigation: { custom_rule: [1, 2], allowed_origins: ["https://iaaa.gbu.edu.cn"] } };
  const original = structuredClone(before);
  let next = setFullscreenValue(before, "browser.lock_fullscreen", true);
  next = setPolicyValue(next, "tunnel_hosts", ["cs101.gbu.edu.cn", "minio.cs101.gbu.edu.cn"]);
  const saved = serializePolicy(next);
  assert.deepEqual(before, original);
  assert.deepEqual(saved.extension, before.extension);
  assert.equal(saved.browser.custom_flag, true);
  assert.deepEqual(saved.navigation, before.navigation);
  assert.equal(saved.browser.require_fullscreen, true);
  assert.equal(saved.browser.lock_fullscreen, true);
});

test("false, empty list, null tunnel and inherited values remain distinct", () => {
  let policy = { browser: { require_fullscreen: false, custom: 7 }, tunnel_hosts: [] };
  assert.deepEqual(serializePolicy(policy), policy);
  policy = setPolicyValue(policy, "browser.require_fullscreen", undefined);
  policy = setPolicyValue(policy, "tunnel_hosts", undefined);
  assert.deepEqual(serializePolicy(policy), { browser: { custom: 7 } });
  assert.deepEqual(serializePolicy({ tunnel_hosts: null }), { tunnel_hosts: null });
});

test("fullscreen controls keep lock and activation consistent", () => {
  const locked = setFullscreenValue({}, "browser.lock_fullscreen", true);
  assert.deepEqual(locked, { browser: { lock_fullscreen: true, require_fullscreen: true } });
  assert.equal(policyValue(setFullscreenValue(locked, "browser.require_fullscreen", false), "browser.lock_fullscreen"), false);
  assert.ok(validatePolicy({ browser: { require_fullscreen: false, lock_fullscreen: true } })["browser.lock_fullscreen"]);
});

test("origins are normalized and de-duplicated independently from tunnel hosts", () => {
  assert.deepEqual(serializePolicy({ navigation: { allowed_origins: [" https://IAAA.gbu.edu.cn/ ", "https://iaaa.gbu.edu.cn", "https://bridge.cs101.gbu.edu.cn"] }, tunnel_hosts: ["CS101.GBU.EDU.CN", "cs101.gbu.edu.cn", ""] }), {
    navigation: { allowed_origins: ["https://iaaa.gbu.edu.cn", "https://bridge.cs101.gbu.edu.cn"] }, tunnel_hosts: ["cs101.gbu.edu.cn"],
  });
  for (const bad of ["iaaa.gbu.edu.cn", "https://iaaa.gbu.edu.cn/login", "https://u:p@iaaa.gbu.edu.cn", "https://@iaaa.gbu.edu.cn", "https://iaaa.gbu.edu.cn?", "https://iaaa.gbu.edu.cn#", "https://iaaa.gbu.edu.cn\\x", "javascript:alert(1)", "https://iaaa.\ngbu.edu.cn"]) {
    assert.ok(validatePolicy({ navigation: { allowed_origins: [bad] } })["navigation.allowed_origins"], bad);
  }
  assert.deepEqual(normalizePolicyList("origins", ["https://example.com:8443/"]), ["https://example.com:8443"]);
});

test("tunnel hosts match native exact-host requirements", () => {
  for (const host of ["cs101.gbu.edu.cn", "minio.cs101.gbu.edu.cn", "11.0.11.2", "xn--fiqs8s.example"]) assert.deepEqual(validatePolicy({ tunnel_hosts: [host] }), {});
  for (const bad of ["https://cs101.gbu.edu.cn", "cs101.gbu.edu.cn:443", "cs101.gbu.edu.cn/x", "*.gbu.edu.cn", "-bad.example", "a..example", "127.1", "127.0.0.01", "0x7f000001", "::1", "中文.example", "a".repeat(64)+".example"]) assert.ok(validatePolicy({ tunnel_hosts: [bad] }).tunnel_hosts, bad);
  assert.ok(validatePolicy({ tunnel_hosts: Array.from({ length: 65 }, (_, i) => `host${i}.example`) }).tunnel_hosts);
});

test("session limits validate integers, bounds and heartbeat relationship", () => {
  for (const heartbeat of [0, 4, 301, 5.5, "15", NaN]) assert.ok(validatePolicy({ session: { heartbeat_seconds: heartbeat } })["session.heartbeat_seconds"]);
  assert.ok(validatePolicy({ session: { max_idle_seconds: 3601 } })["session.max_idle_seconds"]);
  assert.ok(validatePolicy({ session: { heartbeat_seconds: 60, max_idle_seconds: 45 } })["session.max_idle_seconds"]);
  assert.deepEqual(validatePolicy({ session: { heartbeat_seconds: 5, max_idle_seconds: 5 } }), {});
  assert.deepEqual(validatePolicy({ session: { heartbeat_seconds: 300, max_idle_seconds: 3600 } }), {});
});

test("malformed historical sections/lists can be repaired without erasing extensions", () => {
  const policy = { browser: false, navigation: { allowed_origins: "bad", extension: 1 }, tunnel_hosts: [false] };
  assert.ok(validatePolicy(policy).browser);
  assert.ok(validatePolicy(policy)["navigation.allowed_origins"]);
  assert.ok(validatePolicy(policy).tunnel_hosts);
  assert.throws(() => serializePolicy(policy));
  let repaired = setPolicyValue(policy, "browser", undefined);
  repaired = setPolicyValue(repaired, "navigation.allowed_origins", []);
  repaired = setPolicyValue(repaired, "tunnel_hosts", []);
  assert.deepEqual(serializePolicy(repaired), { navigation: { allowed_origins: [], extension: 1 }, tunnel_hosts: [] });
});

test("reserved and legacy fields round-trip, including custom action values", () => {
  const policy = { browser: { allow_print: true, allow_downloads: false }, navigation: { blocked_schemes: ["FILE", "data"], allowed_paths: ["/exam/**"] }, violations: { on_background: "legacy-action", max_background_events: 2 }, allowed_paths: ["/legacy/**"] };
  assert.deepEqual(serializePolicy(policy), { ...policy, navigation: { ...policy.navigation, blocked_schemes: ["file", "data"] } });
  assert.equal(new Set(policyFields.map((field) => field.path)).size, policyFields.length);
});
