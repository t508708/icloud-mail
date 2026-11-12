import assert from "node:assert/strict";
import test from "node:test";
import { loginAppleSession, requestAppleVerificationCode, requestAppleAccountVerificationCode, verifyAppleAccountAuth } from "../src/api/admin.js";

test("web login retains SMS choices and sends only the explicit phone selection", async () => {
  const verification = { method: "sms", phone_id: 7, sent: true, phones: [{ id: 7, label: "受信任手机号 1（尾号 42）" }] };
  const calls = [];
  globalThis.fetch = async (url, options) => {
    calls.push({ url, options });
    return new Response(JSON.stringify({ data: { status: "verification_required", challenge_id: "pending", verification } }), { status: 202, headers: { "Content-Type": "application/json" } });
  };
  const login = await loginAppleSession(12, { apple_id: "fixture", password: "fixture" }, "csrf");
  assert.deepEqual(login.verification, verification);
  const result = await requestAppleVerificationCode(12, { challenge_id: login.challengeId, method: "sms", phone_id: 7 }, "csrf");
  assert.equal(calls.length, 2);
  assert.match(calls[1].url, /\/accounts\/12\/apple-auth\/code$/);
  assert.equal(calls[1].options.method, "POST");
  assert.deepEqual(JSON.parse(calls[1].options.body), { challenge_id: "pending", method: "sms", phone_id: 7 });
  assert.deepEqual(result.verification, verification);
});

test("Account SMS sends and submits to its own channel without another delivery", async () => {
  const calls=[];
  globalThis.fetch=async(url,options)=>{
    calls.push({url,options});
    return new Response(JSON.stringify({data:{status:url.endsWith("/verify")?"authenticated":"verification_required",verification:{method:"sms",phone_id:7,sent:true,phones:[{id:7,label:"受信任手机号 1"}]}}}),{status:200,headers:{"Content-Type":"application/json"}});
  };
  await requestAppleAccountVerificationCode(12,{challenge_id:"pending",method:"sms",phone_id:7},"csrf");
  await verifyAppleAccountAuth(12,{challenge_id:"pending",code:"123456",verification_method:"sms",phone_id:7},"csrf");
  assert.equal(calls.length,2);
  assert.match(calls[0].url,/\/apple-account-auth\/code$/);
  assert.match(calls[1].url,/\/apple-account-auth\/verify$/);
  assert.equal(JSON.parse(calls[1].options.body).verification_method,"sms");
});
