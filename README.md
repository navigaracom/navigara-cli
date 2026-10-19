# Navigara CLI

Connect Claude Code and Codex to Navigara on macOS and Linux.

## 🚀 Install and connect

```sh
curl -fsSL https://navigara.com/cli | bash
navigara wizard
```

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
| Sync Codex only (requires bash or zsh) | `navigara sessions config patch --source codex --apply` |

Run `navigara sessions --help` for more commands.

## 🖥️ MDM

Create an **Analytics Ingestor** key in Navigara. Set `NAVIGARA_TOKEN` only for the per-user setup command below.

Download the installer through MDM and run it as root:

```sh
curl -fsSL https://navigara.com/cli -o navigara-install.sh
bash navigara-install.sh --system
```

Run in each employee’s account, without `sudo`, using their email:

```sh
/opt/navigara/bin/navigara sessions config patch --email employee@example.com --apply
```

If Claude is already managed by MDM, add `--source codex` to this command.

## 🔐 Account

- Sign in: `navigara login`
- Sign out: `navigara logout`
