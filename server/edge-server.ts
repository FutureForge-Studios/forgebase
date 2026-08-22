// ForgeBase Edge Functions warm runner. pgforged starts this ONCE per
// function and proxies requests to it over localhost:
//   deno run --allow-net --allow-env=... --allow-read=/opt/pgforge-functions \
//     edge-server.ts <function-file> <port>
// Deno.serve means no per-request process boot (warm starts), and the proxy
// passes bodies through as they are produced - streaming responses,
// WebSocket upgrades and post-response background work all behave exactly
// as they do in a standalone Deno server.
const funcPath: string = Deno.args[0];
const port = parseInt(Deno.args[1], 10);

const mod = await import("file://" + funcPath);
const handler = mod.default;
if (typeof handler !== "function") {
  throw new Error("function must `export default` a handler");
}

Deno.serve(
  { hostname: "127.0.0.1", port },
  async (req: Request) => {
    try {
      return await handler(req);
    } catch (e) {
      return new Response(
        JSON.stringify({ error: String((e as Error)?.message ?? e) }),
        { status: 500, headers: { "content-type": "application/json" } },
      );
    }
  },
);
