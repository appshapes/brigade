// Standard-Webhooks (Svix) signature check, by hand: HMAC-SHA256 over `${id}.${timestamp}.${body}` with the
// base64-decoded secret (the `whsec_` prefix removed), base64 output, compared in constant time against each
// `v1,<sig>` entry of the space-separated signature header. Resend signs its webhooks this way.

export async function verifySvix(
  secret: string,
  headers: Headers,
  body: string,
  nowSeconds = Math.floor(Date.now() / 1000),
  toleranceSeconds = 300,
): Promise<boolean> {
  const id = headers.get("svix-id") ?? headers.get("webhook-id");
  const ts = headers.get("svix-timestamp") ?? headers.get("webhook-timestamp");
  const sigs = headers.get("svix-signature") ?? headers.get("webhook-signature");
  if (!id || !ts || !sigs) return false;
  const t = Number(ts);
  if (!Number.isFinite(t) || Math.abs(nowSeconds - t) > toleranceSeconds) return false;
  let expected: string;
  try {
    expected = await hmacBase64(stripPrefix(secret), `${id}.${ts}.${body}`);
  } catch {
    return false;
  }
  for (const part of sigs.split(" ")) {
    const comma = part.indexOf(",");
    if (comma < 0) continue;
    if (part.slice(0, comma) === "v1" && constantTimeEqual(part.slice(comma + 1), expected)) return true;
  }
  return false;
}

/** signSvix produces the header value a sender would, for tests and for a provider that needs one. */
export async function signSvix(secret: string, id: string, ts: string, body: string): Promise<string> {
  return "v1," + (await hmacBase64(stripPrefix(secret), `${id}.${ts}.${body}`));
}

function stripPrefix(secret: string): string {
  return secret.startsWith("whsec_") ? secret.slice("whsec_".length) : secret;
}

/** hmacBase64 is HMAC-SHA256 of text under the base64-encoded key, base64-encoded. */
async function hmacBase64(keyBase64: string, text: string): Promise<string> {
  const bytes = atob(keyBase64);
  const key = new Uint8Array(new ArrayBuffer(bytes.length));
  for (let i = 0; i < bytes.length; i++) key[i] = bytes.charCodeAt(i);
  const k = await crypto.subtle.importKey("raw", key, { name: "HMAC", hash: "SHA-256" }, false, ["sign"]);
  const mac = new Uint8Array(await crypto.subtle.sign("HMAC", k, new TextEncoder().encode(text)));
  return btoa(String.fromCharCode(...mac));
}

function constantTimeEqual(a: string, b: string): boolean {
  if (a.length !== b.length) return false;
  let diff = 0;
  for (let i = 0; i < a.length; i++) diff |= a.charCodeAt(i) ^ b.charCodeAt(i);
  return diff === 0;
}
