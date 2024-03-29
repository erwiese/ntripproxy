# ntripproxy

[![Open in Visual Studio Code](https://open.vscode.dev/badges/open-in-vscode.svg)](https://open.vscode.dev/erwiese/ntripproxy)

Reverse proxy for Ntrip broadcasters


The proxy returns a combined sourcetable from all backend casters. 

Description:
   see https://kasvith.github.io/posts/lets-create-a-simple-lb-go/ whereof the code is adopted, thanks Kasun.

Example:

To start the proxy with the following NtripCaster backends
- http://localhost:2101
- http://localhost:2102
```bash
ntripproxy -backends=http://localhost:2101,http://localhost:2102
```

see also
- https://hackernoon.com/writing-a-reverse-proxy-in-just-one-line-with-go-c1edfa78c84b
- https://medium.com/@mlowicki/http-s-proxy-in-golang-in-less-than-100-lines-of-code-6a51c2f2c38c
- https://golang.org/pkg/net/http/httputil/#ReverseProxy
