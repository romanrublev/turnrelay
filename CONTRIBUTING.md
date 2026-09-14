# Contributing

[English](CONTRIBUTING.md) | [Русский](CONTRIBUTING.ru.md)


turnrelay is in beta and contributions are welcome.

## Ground rules

- **Scope.** This is a censorship-circumvention transport for research and
  personal use. Contributions should serve that; no account-farming,
  captcha-solving-as-a-service, or mass-targeting tooling.
- **Licensing.** The project is GPL-3.0. Only contribute code you can license
  under GPL-3.0. Do not paste code from PolyForm-Noncommercial or other
  incompatible sources; where you port from a GPL project, note it in a file
  header and in `NOTICE`.

## Development

```bash
make test    # go test -race ./...
make vet     # go vet ./...
make build   # bin/turnrelay-udp
```

- Tests run in-process against local stand-ins (a pion/turn server, obfs
  servers) and never touch VK. Keep it that way; a live-VK test belongs in a
  manual runbook, not `go test`.
- New behaviour comes with a test. Match the surrounding style.

## Reporting

- Bugs and protocol changes (VK evolves its API - breakage there is common):
  open an issue with the relevant log lines. Do not paste real call links,
  credentials, or server IPs.
