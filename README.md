# Navigara CLI

Connect Claude Code and Codex to Navigara. Import past sessions and keep new activity in sync.

## 🚀 Install and connect

Requires macOS or Linux, Git, Go 1.27+, and access to this repository.
Run Claude Code or Codex once before connecting.

```sh
git clone https://github.com/navigaracom/navigara-cli.git
cd navigara-cli
GOBIN="$HOME/.local/bin" go install .
mv "$HOME/.local/bin/navigara-cli" "$HOME/.local/bin/navigara"
export PATH="$HOME/.local/bin:$PATH"
navigara wizard
```

Sign in and choose your organization. The wizard preselects detected tools,
your email, and all history. Keep the defaults or choose **Do not import history**.
Creating an ingestion key requires organization admin access.

Add `~/.local/bin` to your shell's PATH for future terminals. After setup, restart
your tools in a new terminal; launch desktop and IDE tools from there too.
Your previous settings are saved in private backups.

For your company's Navigara instance:

```sh
navigara wizard --host https://navigara.example.com
```

On-prem administrators: set the server's `FRONTEND_URL` to the same address.

## 📡 Stream new activity only

```sh
navigara login
navigara sessions config patch           # preview which files will change
navigara sessions config patch --apply   # apply settings
```

Add `--source claude` or `--source codex` to either `config patch` command to configure one tool.
For your own instance, add `--host https://navigara.example.com` to `login`.
Restart your tools in a new terminal after applying settings.

## 🖥️ Deploy with MDM

Package an approved `navigara` binary for each target OS and processor.
From the package directory, install it machine-wide:

```sh
sudo mkdir -p /opt/navigara/bin
sudo install -m 0755 ./navigara /opt/navigara/bin/navigara
```

Each user connects in their own account, without `sudo`:

```sh
/opt/navigara/bin/navigara login
/opt/navigara/bin/navigara sessions config patch --apply
```

Sign-in opens the user's browser. Creating an ingestion key requires organization admin access.
If Claude settings are managed by your company, configure telemetry in your MDM
policy; the CLI does not change managed settings.

## What gets sent

- **History:** usage, costs, session activity, identity, and repository metadata.
  Prompt text, tool inputs, and outputs are omitted.
- **Live Claude sessions:** prompt text and tool names, arguments, and commands.
  Assistant responses, tool output, raw API bodies, and traces stay disabled.

## Help

`navigara --help` lists commands. To retry an interrupted import, use the
directory printed by the CLI:

```sh
navigara wizard --resume '/path/to/saved/payload'
```
