import { assertEquals } from "jsr:@std/assert@1";
import { signSvix, verifySvix } from "./svix.ts";

const secret = "whsec_" + btoa("0123456789abcdef0123456789abcdef");

Deno.test("a signature made with the secret verifies, a tampered body does not", async () => {
  const body = '{"type":"email.received"}';
  const ts = "1700000000";
  const sig = await signSvix(secret, "msg_1", ts, body);
  const h = new Headers({ "svix-id": "msg_1", "svix-timestamp": ts, "svix-signature": `v2,junk ${sig}` });
  assertEquals(await verifySvix(secret, h, body, 1700000010), true);
  assertEquals(await verifySvix(secret, h, body + " ", 1700000010), false);
  assertEquals(
    await verifySvix("whsec_" + btoa("another secret value here 1234"), h, body, 1700000010),
    false,
  );
});

Deno.test("a stale timestamp or a missing header fails", async () => {
  const body = "{}";
  const sig = await signSvix(secret, "m", "1700000000", body);
  const h = new Headers({ "svix-id": "m", "svix-timestamp": "1700000000", "svix-signature": sig });
  assertEquals(await verifySvix(secret, h, body, 1700001000), false);
  assertEquals(await verifySvix(secret, new Headers({ "svix-id": "m" }), body, 1700000000), false);
});
