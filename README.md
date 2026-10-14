# Navigara CLI

Connect Claude Code and Codex to Navigara. Import past sessions and keep new activity in sync.

## 🚀 Install and connect

On macOS or Linux, download the installer, review it, then run it from disk:

```sh
curl -fsSL https://navigara.com/cli -o navigara-install.sh
bash navigara-install.sh
export PATH="$HOME/.local/bin:$PATH"
navigara wizard
```

Sign in through your browser and choose your organization. The wizard preselects detected tools,
your email, and all history. Keep the defaults or choose **Do not import history**.
The wizard creates an ingestion key, which requires organization admin access.

Add `~/.local/bin` to your shell's PATH for future terminals. After setup, restart
your tools in a new terminal; launch desktop and IDE tools from there too.
Your previous settings are saved in private backups. Codex setup requires bash or zsh.

For your company's instance, use its address (the server's `FRONTEND_URL` must match):

```sh
navigara wizard --host https://navigara.example.com
```

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

An administrator creates one analytics ingestor key in the Navigara dashboard.
The key determines the organization. Have MDM inject it as `NAVIGARA_TOKEN` into
each user's setup environment; employees do not need to sign in.

Distribute the reviewed installer through MDM and run it as root:

```sh
bash navigara-install.sh --system
```

Run configuration in each user's account, without `sudo`. Replace the example
email with that employee's email. Preview is offline; apply checks the connection
before changing settings:

```sh
/opt/navigara/bin/navigara sessions config patch --email employee@example.com
/opt/navigara/bin/navigara sessions config patch --email employee@example.com --apply
```

For managed Claude settings, configure Claude telemetry through your MDM policy.
The default patch stops at managed Claude. To configure Codex only:

```sh
/opt/navigara/bin/navigara sessions config patch --source codex --email employee@example.com --apply
```

For your own instance, add `--host https://navigara.example.com` to these commands.
Restart the user's tools in a new terminal afterward.

## What gets sent

- **History:** usage, costs, session activity, identity, and repository metadata.
  Prompt text, tool inputs, and outputs are omitted.
- **Live Claude sessions:** prompt text and tool names, arguments, and commands.
  Assistant responses, tool output, raw API bodies, and traces stay disabled.

Run `navigara sessions --help` for upload and streaming commands. To retry an
interrupted import, use the directory printed by the CLI:

```sh
navigara wizard --resume '/path/to/saved/payload'
```

<details>
<summary>Optional: install from source</summary>

Requires Git, Go 1.27+, and access to this repository.

```sh
git clone https://github.com/navigaracom/navigara-cli.git
cd navigara-cli
GOBIN="$HOME/.local/bin" go install .
mv "$HOME/.local/bin/navigara-cli" "$HOME/.local/bin/navigara"
export PATH="$HOME/.local/bin:$PATH"
```

</details>
