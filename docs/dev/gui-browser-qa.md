# GUI Browser QA

The browser client is the primary manual QA path for Kent GUI work. It exercises the real React app against a Kent server.

Run it from the repo root:

```sh
just dev desktop
```

Open:

```text
http://127.0.0.1:1420/
```

By default the browser client connects to the existing local Kent server:

```text
ws://127.0.0.1:53082/rpc
```

For a one-off endpoint override, pass `appRpcEndpoint`:

```text
http://127.0.0.1:1420/?appRpcEndpoint=ws%3A%2F%2F127.0.0.1%3A53082%2Frpc
```

For a dev-server-wide override, set:

```sh
VITE_APP_RPC_ENDPOINT=ws://127.0.0.1:53082/rpc just dev desktop
```
