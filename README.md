# Navigara CLI

Connect Claude Code and Codex to Navigara on macOS, Linux, and Windows.

## 🚀 Install and connect

```sh
curl -fsSL https://navigara.com/cli | sh
```

On Windows, run this in PowerShell:

```powershell
irm https://github.com/navigaracom/navigara-cli/releases/latest/download/install.ps1 | iex
```

On Windows, setup saves the Codex email in the user environment variable `OTEL_RESOURCE_ATTRIBUTES`, not in a shell profile.

| To… | Run… |
| --- | --- |
| Connect to your company’s Navigara instance | `navigara wizard --host https://navigara.example.com` |

## 📡 Sessions

| To… | Run… |
| --- | --- |
| Import all history | `navigara sessions upload` |
| Import the past week | `navigara sessions upload --last 7d` |
| Import only Claude history | `navigara sessions upload --source claude` |
| Import only Codex history | `navigara sessions upload --source codex` |
| See what would be sent, without sending | `navigara sessions upload --dry-run` |
| Send the rest of a stopped upload | `navigara sessions upload --resume DIR` |
| Enable future sync | `navigara sessions config patch --apply` |
| Sync Codex only | `navigara sessions config patch --source codex --apply` |
| Replace telemetry that Claude or Codex already sends elsewhere | `navigara sessions config patch --apply --replace` |
| Pick the shell profile for the Codex identity | `navigara sessions config patch --apply --shell fish` |
| Stop future sync | `navigara sessions config remove --apply` |
| Check that sync works (exit code 1 if not) | `navigara sessions status` |

Each run saves the previous files in one folder: `~/.config/navigara/backups/<timestamp>/`.

Run `navigara sessions --help` for more commands.

## 🖥️ MDM

### Windows (Intune)

Add a PowerShell script in Intune. Set **Run this script using the logged on credentials** to **Yes**.

```powershell
$env:NAVIGARA_TOKEN = '<key>'
& ([scriptblock]::Create((irm https://github.com/navigaracom/navigara-cli/releases/latest/download/install.ps1))) -NavigaraHost https://app.navigara.com
```

The script installs the CLI for the signed-in user, configures telemetry, and uploads session history.
It exits with 3 when the user has no signed-in Claude Code or Codex account yet. Run it again later for that user.

### macOS and Linux

Create an **Analytics Ingestor** key in Navigara. Set `NAVIGARA_TOKEN` only for the setup command below.

Download the installer through MDM and run it as root:

```sh
curl -fsSL https://navigara.com/cli -o navigara-install.sh
sh navigara-install.sh --system
```

Then run one command as root:

```sh
NAVIGARA_TOKEN=<key> /opt/navigara/bin/navigara mdm --host https://app.navigara.com
```

The command finds every user on the computer who has Claude Code or Codex data.
It configures telemetry and uploads session history for each of them.
The email comes from the Claude Code account signed in for that user, then the Codex account.
A failure for one user does not stop the others. The command exits with 1 if any user failed.
Users who never opened Claude Code or Codex are configured on the next MDM run.
You can run the command again at any time.

If Claude is already managed by MDM, add `--source codex` to the command.

## 🔐 Account

- Sign in: `navigara login`
- Sign out: `navigara logout`
