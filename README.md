# Navigara CLI

On macOS or Linux, connect Claude Code and Codex to Navigara. Import past sessions and keep new activity in sync.

## Install and connect

```sh
curl -fsSL https://navigara.com/cli | bash
navigara wizard
```

Sign in through your browser, choose an organization, then accept the detected tools, email, and history selection. Choose **Do not import history** to stream only new activity. The wizard creates an ingestion key and requires organization admin access.

Restart your tools from a new terminal after setup. Private backups preserve earlier settings. Codex setup requires bash or zsh.

| Wizard flag | Meaning |
| --- | --- |
| `--host ORIGIN` | Navigara instance; defaults to the saved host or `https://app.navigara.com`. Also accepted by `login`. |
| `--resume DIR` | Retry the exact saved setup batches in `DIR`. |

## Login and sessions

Sign in, send sessions, and apply streaming settings:

```sh
navigara login
navigara sessions upload --send
navigara sessions config patch --apply
```

Run `navigara sessions upload` without `--send` to prepare files. `navigara logout` signs out. Run `navigara sessions --help` for more.

| Command scope | Flag | Meaning |
| --- | --- | --- |
| `upload` | `--host ORIGIN` | Origin for `--send` with a saved login. |
| `upload` | `--url URL` | Base URL for token uploads. |
| `upload`, `config patch` | `--token TOKEN` | Analytics ingestor token; also reads `NAVIGARA_TOKEN`. |
| `upload`, `config patch` | `--email EMAIL` | Contributor email. Required for token-based patches. |
| `upload`, `config patch` | `--source all\|claude\|codex` | Defaults to `all`. For patches with managed Claude settings, select `codex`; Claude follows the MDM policy. |
| `upload`, `config patch` | `--claude-home DIR`, `--codex-home DIR` | Override the Claude or Codex home. |
| `upload` | `--last DURATION`, `--since TIME`, `--until TIME` | Limit records by `24h` or `7d`, or by RFC 3339 start and end times. |
| `upload` | `--session ID` | Export one session; repeatable. |
| `upload` | `--output DIR` | New directory for prepared session files. |
| `upload` | `--send` | Post request bodies using the saved login or `--url` and `--token`. |
| `config patch` | `--host ORIGIN` | Navigara instance; defaults to the saved host with login, or `https://app.navigara.com` with a token. |
| `config patch` | `--apply` | Write the prepared settings; omit to preview. |

## Deploy with MDM

An administrator creates an analytics ingestor key in Navigara. Have MDM inject the existing key as `NAVIGARA_TOKEN` into each user's setup environment; employees do not need to sign in.

Download the installer through MDM and run it as root:

```sh
curl -fsSL https://navigara.com/cli -o navigara-install.sh
bash navigara-install.sh --system
```

Run the patch in each user's account, without `sudo`:

```sh
/opt/navigara/bin/navigara sessions config patch --email employee@example.com --apply
```

## What gets sent

- **History:** usage, costs, session activity, identity, and repository metadata. Prompt text, tool inputs, and outputs are omitted.
- **Live Claude sessions:** prompt text plus tool names, arguments, and commands. Assistant responses, tool output, raw API bodies, and traces stay disabled.

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
