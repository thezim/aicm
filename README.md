# go-aws

A terminal UI for listing, starting, and stopping EC2 instances, opening interactive SSM console sessions, and launching RDP sessions to Windows instances through AWS SSM Session Manager port forwarding.

`go-aws` resolves your normal AWS configuration, lists instances in one account/region, and can start or stop instances from the list. It can either run `aws ssm start-session` in the foreground for an interactive console or start a port-forwarding tunnel in the background, write a temporary `.rdp` file, and hand it to your operating system's RDP client. A per-user daemon owns RDP tunnels so they keep running after the TUI exits.

```
TUI client ── aws ssm start-session (console, foreground)
     │
     └── local IPC ── daemon
                       ├── aws ssm start-session (one process per RDP tunnel)
                       ├── .rdp files
                       └── OS RDP opener
```

## Features

- Scrollable EC2 list with a Name column and a separate instance ID column between state and type. Instances without a Name tag show their ID in the Name column.
- All EC2 instances are listed by default, regardless of state. An optional `aws.states` filter can restrict the list.
- Live `/` filtering of the Instances and Connections tables. Every keypress filters immediately against any column value, including columns hidden on narrow terminals.
- Start (`s`) stopped instances and stop (`S`) running instances directly from the list. Stop requires a second `S` and warns when active tunnels will close.
- Duplicate names get deterministic `-01`, `-02`, … suffixes.
- Built-in theme plus global (`app.colors`) and per-account color overrides for lists, selection, table headers, the bottom status bar, the transient status line, the key-binding footer, and Help.
- Interactive SSM console sessions (`c`) for running SSM-managed instances.
- Instance info screen (`i`) with state, network, platform, launch time, tunnel count, and alphabetically sorted EC2 tags.
- RDP sessions (`r`) for **running Windows** instances only, with per-account username, domain, resolution, full-screen, multi-monitor, clipboard, drives, printers, smartcards, USB, webcam, audio, and microphone settings.
- Every `r` starts a **new** tunnel, including multiple tunnels to the same instance.
- Auto-starts and reuses one background daemon; RDP tunnels survive TUI exit.
- Edit the active config file (`e`) in `$VISUAL`, `$EDITOR`, a configured editor, or the OS default, then reload it without restarting.
- Daemon exits automatically after the last tunnel and client disappear.
- Cleanup on normal close, daemon shutdown, AWS CLI exit, startup failure, or timeout.
- macOS, Linux, and Windows support.

## Requirements

- Go 1.26+ to build.
- AWS CLI v2 with the Session Manager plugin. The TUI and daemon invoke the `aws` executable from `PATH` (override with `GO_AWS_AWS_BIN` for testing).
- SSM Agent running on the target EC2 instance and network connectivity to SSM.
- For RDP: a Windows instance with RDP enabled (port 3389 by default). RDP is intentionally unavailable for non-Windows instances and is refused by both the TUI and the daemon.
- An RDP client registered with the OS for `.rdp` files. On Linux you may need to configure `app.rdp_opener`.
- IAM permission to call `ec2:DescribeInstances`, `ec2:StartInstances`, and `ec2:StopInstances`, plus SSM permissions for console and port-forwarding sessions.

## Build

```sh
go build -o go-aws ./cmd/go-aws
go test ./...
```

Cross-compiles with CGO disabled:

```sh
GOOS=darwin  GOARCH=arm64 CGO_ENABLED=0 go build ./cmd/go-aws
GOOS=linux   GOARCH=amd64 CGO_ENABLED=0 go build ./cmd/go-aws
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build ./cmd/go-aws
```

## Usage

```text
go-aws [--config PATH] [--profile NAME] [--region NAME]
go-aws daemon run [--config PATH]
go-aws daemon status
go-aws daemon stop
go-aws connections [--json]
go-aws close <connection-id> | --all
go-aws version
```

### TUI keys

| Key | Action |
|---|---|
| `↑`/`↓`, `j`/`k` | Move selection |
| `PgUp`/`PgDn`, `Home`/`End`, `g`/`G` | Page / jump |
| Mouse wheel | Scroll |
| `/` | Filter the current table; type to filter live, `Enter` keeps it, `Esc` cancels/clears |
| `i` | Show full details and tags for the selected instance |
| `c` | Open an interactive SSM console session (running instances) |
| `r` | Start a new SSM port-forwarding tunnel and open RDP (Windows, running instances) |
| `s` | Start the selected stopped instance |
| `S` then `S` | Stop the selected running instance (second press confirms; `esc`/`n` cancels) |
| `R` | Refresh the instance list |
| `Tab` | Switch between Instances and Connections |
| `o` / `Enter` | Reopen the stored `.rdp` file (Connections) |
| `x` then `x` | Close a tunnel after confirmation |
| `e` | Edit the config file in your editor, then reload it |
| `?` | Help |
| `q` / `Ctrl+C` | Quit (RDP tunnels keep running) |

The app name, account, region, profile, tunnel count, and daemon state are shown in the status bar on the bottom line.

Pressing `/` filters the current table case-insensitively across every column value, including columns that are hidden on a narrow terminal. Non-matching rows disappear immediately after each keypress. `Backspace` edits the query, `Ctrl+U` clears it, `Enter` keeps the filter, `Esc` while editing restores the previous filter, and `Esc` in normal mode clears the active filter. Each table keeps its own filter when you switch tabs.

Pressing `r` on a non-Windows instance shows a hint to use `c` instead. Console and RDP actions require the instance to be `running`; selecting a stopped instance shows a hint to press `s` to start it. Console sessions run in the foreground: the TUI temporarily releases the terminal, runs `aws ssm start-session --target <instance-id>`, and returns to the list when you exit the shell.

Pressing `i` opens a read-only detail screen for the selected instance, including its EC2 tags sorted alphabetically by key and displayed in aligned `KEY` and `VALUE` columns. Use `↑`/`↓`, `PgUp`/`PgDn`, or `Home`/`End` to scroll, and `i` or `esc` to return to the list.

Instance actions are state-gated: `s` only starts `stopped` instances and `S` only stops `running` instances. `pending`, `stopping`, `shutting-down`, and `terminated` instances are refused with a status message, and only one power action can run at a time. The list is refreshed immediately after an action; use `R` to refresh again later, because the TUI does not poll.

By default the TUI exits after the RDP file is opened successfully. Set `app.quit_after_rdp: false` to keep it open. Console sessions never trigger the automatic exit.

## AWS authentication and region

`go-aws` uses the standard AWS SDK v2 chain first:

- `AWS_PROFILE`, `AWS_REGION`, `AWS_DEFAULT_REGION`
- environment credentials
- `~/.aws/config` and `~/.aws/credentials`
- SSO token cache
- web identity / OIDC
- ECS container credentials
- EC2 instance metadata

`--profile` and `--region` are only fallbacks when the chain cannot resolve them. A profile is not required when environment or instance credentials already work.

```sh
# SSO, explicit fallback flags
aws sso login --profile prod
go-aws --profile prod --region us-east-1

# Environment credentials; no flags needed
export AWS_ACCESS_KEY_ID=...
export AWS_SECRET_ACCESS_KEY=...
export AWS_SESSION_TOKEN=...
export AWS_REGION=us-east-1
go-aws
```

The AWS account ID is resolved with `sts:GetCallerIdentity`. That account ID is the config key: multiple profiles/roles in the same account share one entry.

### IAM

Minimum policy shape (replace region/account as needed):

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": [
        "ec2:DescribeInstances",
        "ec2:StartInstances",
        "ec2:StopInstances"
      ],
      "Resource": "*"
    },
    {
      "Effect": "Allow",
      "Action": "ssm:StartSession",
      "Resource": [
        "arn:aws:ec2:us-east-1:123456789012:instance/*",
        "arn:aws:ssm:us-east-1::document/AWS-StartPortForwardingSession",
        "arn:aws:ssm:us-east-1::document/SSM-SessionManagerRunShell"
      ]
    },
    {
      "Effect": "Allow",
      "Action": [
        "ssm:TerminateSession",
        "ssm:ResumeSession"
      ],
      "Resource": "arn:aws:ssm:*:*:session/${aws:username}-*"
    }
  ]
}
```

`sts:GetCallerIdentity` needs no allow statement. The instance's own role needs SSM Agent access (for example, `AmazonSSMManagedInstanceCore`).

## Configuration

Default path: `~/.config/go-aws/config.yml` on all platforms (on Windows: `%USERPROFILE%\.config\go-aws\config.yml`). Override with `--config PATH`.

The file is created/updated automatically the first time an AWS account is resolved. Existing account entries are never rewritten. Unknown keys and comments are preserved when new accounts are added.

```yaml
aws:
  states: []          # empty = all states; e.g. [running, stopped] to filter

app:
  quit_after_rdp: true
  rdp_opener: []          # empty = OS association; {file} is replaced with the .rdp path
  editor: []              # empty = $VISUAL, then $EDITOR, then the OS default;
                          # otherwise an argv list containing {file}, e.g. ["code", "--wait", "{file}"]
  colors:                 # global theme; accounts can override individual keys
    list_foreground: "#cdd6f4"
    list_background: "#1e1e2e"
    selected_foreground: "#1e1e2e"
    selected_background: "#89b4fa"
    header_foreground: "#89b4fa"
    header_background: "#1e1e2e"
    status_foreground: "#89b4fa"        # bottom status bar
    status_background: "#1e1e2e"
    transient_foreground: "#89b4fa"     # messages, spinner, filter prompt
    transient_background: "#1e1e2e"
    transient_error_foreground: "#f38ba8"
    keys_foreground: "#89b4fa"          # key-binding footer
    keys_background: "#1e1e2e"
    help_foreground: "#cdd6f4"          # Help screen body
    help_background: "#1e1e2e"

accounts:
  "123456789012":
    colors:               # optional per-account overrides
      selected_background: "#a6e3a1"
      transient_background: "#181825"
      keys_background: "#11111b"
    rdp:
      username: ""
      domain: ""
      remote_port: 3389
      full_screen: false
      resolution: "1920x1080"
      multimonitor: false
      clipboard: true
      drives: false
      printers: false
      smartcards: false
      usb_devices: false
      webcam: false
      audio: local        # local | remote | none
      microphone: false

daemon:
  connect_timeout: 30s
  idle_exit: 5s
```

- Account IDs must be quoted in YAML.
- `aws.states` is an optional filter. An empty list (the default) lists instances in every state; explicit values such as `[running]` or `[running, stopped]` restrict the list.
- Colors accept `#RRGGBB`, ANSI number `0`–`255`, or `default`.
- Color resolution is built-in defaults → `app.colors` → `accounts.<id>.colors`. Per-account values win; omit a key to inherit the global value. Configs created by older versions may contain a full per-account `colors` block; delete those entries to keep following `app.colors`.
- Color groups are `list_*` and `selected_*` for rows/selection, `header_*` for the table header, `status_*` for the bottom status bar, `transient_*` (plus `transient_error_foreground`) for messages/spinner/errors/filter prompt, `keys_*` for the key-binding footer, and `help_*` for the Help body. `status_error_foreground` was renamed to `transient_error_foreground`.
- Invalid values fall back to built-in defaults with a warning; malformed YAML is fatal and is never overwritten.
- `aws.profile` and `aws.region` are intentionally not config keys. Use the AWS chain or the CLI flags.
- `app.rdp_opener` is an argv template executed without a shell, for example:
  ```yaml
  app:
    rdp_opener: ["remmina", "-c", "{file}"]
  ```
  On Windows the default uses `cmd /c start "" <file>`.
- `app.editor` is an argv template containing `{file}`, for example `["code", "--wait", "{file}"]`. When empty, `$VISUAL` and then `$EDITOR` are used, followed by the OS default. Press `e` to open the config in that editor; the TUI reloads it when the editor exits.

## Daemon and cleanup

- Console sessions run directly in the TUI's terminal and are not managed by the daemon; they end when you exit the remote shell.
- One daemon per user. macOS/Linux use a Unix socket in a `0700` directory; Windows uses a named pipe restricted to the current user.
- The TUI resolves short-lived AWS credentials and sends them to the daemon only for the new RDP tunnel child. They are never written to disk, logged, or returned in state/events.
- The daemon strips inherited AWS authentication variables before starting each RDP tunnel; console sessions use the same sanitized environment.
- Tunnels are owned by process groups (Unix) or Job Objects (Windows), so `session-manager-plugin` children are also terminated.
- Closing a tunnel, an AWS CLI exit, a connect timeout, or daemon shutdown removes the `.rdp` file and emits a close event.
- Crash-recovery state contains PIDs, ports, commands, and `.rdp` paths only; it is removed on normal shutdown.
- Daemon logs and cache data live under the OS cache directory:
  - macOS: `~/Library/Caches/go-aws/`
  - Linux: `${XDG_CACHE_HOME:-~/.cache}/go-aws/`
  - Windows: `%LocalAppData%\go-aws\`

## Troubleshooting

- `daemon: not running` — start any command that uses the daemon, or run `go-aws daemon run` manually for debugging.
- Check the daemon log (`daemon.log` in the cache directory) for spawn failures.
- Console session exits immediately — confirm the instance is managed by SSM (`aws ssm describe-instance-information`), the SSM Agent is running, and the instance role allows `ssm:StartSession` with the `SSM-SessionManagerRunShell` document (or your account's custom default session document).
- "SSM session did not become ready" — confirm the instance is managed by SSM (`aws ssm describe-instance-information`), the SSM Agent is running, and the instance role has SSM permissions.
- `SessionManagerPlugin is not found` — install the AWS Session Manager plugin and ensure `session-manager-plugin` is in `PATH`.
- No region resolved — pass `--region` or set `AWS_REGION`/`AWS_DEFAULT_REGION`.
- `r` says RDP is only for Windows — the instance platform reported by EC2 is not Windows. Use `c` for a console session.
- `c` or `r` says the instance is not running — start it with `s` and wait for it to reach `running`.
- `s`/`S` refuses the action — start requires `stopped` and stop requires `running`; `pending`, `stopping`, `shutting-down`, and `terminated` are not actionable. Wait or refresh with `R`.
- Stopping an instance closes its active tunnels — the confirmation warns before doing so.
- RDP opens but cannot connect — verify the remote host allows RDP, and that security groups/VPC endpoints allow SSM connectivity.
- On Linux, set `app.rdp_opener` if `.rdp` files are not associated with an RDP client.

## Limitations (v1)

- One AWS account and one region per TUI invocation.
- RDP is available only for instances whose EC2 platform is Windows.
- Console sessions are foreground-only and end when the shell exits; RDP tunnels are the only daemon-managed connections.
- No autoreconnect after an SSM session ends.
- No detection of RDP client exit; use the Connections view or `go-aws close` to stop a tunnel.
- No password storage or autofill; the RDP client prompts for credentials.
- EC2 lifecycle support is limited to start and stop; terminate, reboot, and hibernate are out of scope.
