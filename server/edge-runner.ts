// ForgeBase Edge Functions runner. pgforged invokes:
//   deno run --allow-net --allow-env --allow-read=/opt/pgforge-functions \
//     edge-runner.ts <function-file>
// It reads one request as JSON from stdin, calls the function's default export
// (a Fetch-style handler), and writes the response as JSON to stdout.
const funcPath: string = Deno.args[0];

try {
  const mod = await import("file://" + funcPath);
  const handler = mod.default;
  if (typeof handler !== "function") {
    throw new Error("function must `export default` a handler");
  }
  const raw = await new Response(Deno.stdin.readable).text();
  const input = JSON.parse(raw || "{}");
  const method = (input.method || "GET").toUpperCase();
  const req = new Request("http://fn" + (input.url || "/"), {
    method,
    headers: input.headers || {},
    body: input.body && method !== "GET" && method !== "HEAD" ? input.body : undefined,
  });
  const res: Response = await handler(req);
  const body = await res.text();
  const headers: Record<string, string> = {};
  res.headers.forEach((v, k) => (headers[k] = v));
  console.log(JSON.stringify({ status: res.status, headers, body }));
} catch (e) {
  console.log(JSON.stringify({
    status: 500,
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ error: String(e && (e as Error).message || e) }),
  }));
}
