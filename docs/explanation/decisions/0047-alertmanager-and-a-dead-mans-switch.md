# ADR 0047: Alertmanager, and a dead man's switch so that silence means something

- Status: Proposed (no code yet; each phase is its own change)
- Date: 2026-11-24
- Follows: [ADR 0019](0019-fleet-metrics.md), [ADR 0025](0025-operator-ses-smtp.md), [ADR 0046](0046-backup-coverage-of-hosts-outside-the-cluster.md)

## Context

mksrv ships 17 Prometheus alert rules (`stacks/monitor/templates/prometheus-rules.yml.tmpl`): 7 `critical` (a Patroni cluster
with no leader, a sealed OpenBao, an endpoint down, a stale or incomplete backup, uncovered critical volume snapshots) and 10
`warning`. **Nothing delivers any of them.** Checked on the live fleet on 2026-11-24:

- Prometheus reports **0 Alertmanagers** (`/api/v1/alertmanagers`), the configuration has no `alerting:` block, and no
  Alertmanager container exists. mksrv provisions no Grafana alerting either (the monitor stack renders only datasources and dashboards).
- This is not hypothetical. **Nine alerts fired from 2026-11-22 04:46 UTC until they were fixed on 2026-11-24** (the postgres and
  Caddy exporters unreachable, five endpoint probes), and `BackupStale`/`BackupComponentFailed` were added and tuned in the same
  days. Nobody was told by the system: they were found by reading Prometheus by hand while rolling out something else.
- The alerts that matter most are the quietest ones: a backup that stopped, a snapshot policy that selects nothing, a cluster
  without a leader. Their whole value is in being delivered.

What is already in place and can be used:

- **Outbound mail that does not depend on the fleet.** `mail.outbound_smtp: true` (ADR 0025) gave the operator an SES SMTP
  credential, kept in SSM (`/mksrv/<env>/mail/ses_smtp_user` and `ses_smtp_password`), with the sender `noreply@<root_domain>`.
  The SES account has **production access** (sending enabled, 50,000 messages a day quota, none sent in the last 24 h), so a
  recipient does not need to be a verified identity.
- A monitor stack (`prometheus`, `grafana`, `blackbox`) on `appd`, with `--web.enable-lifecycle`, reload hooks, and a
  `tailnet_ports` convention for what the mesh may reach.

What is not obvious and shapes the design:

- **The monitor host is a single point of failure for the monitoring itself.** If `appd` is down, or Prometheus or Alertmanager
  hangs, no alert fires and the silence looks the same as health. Alertmanager alone does not solve that.
- **The edge cannot be the sender.** The fleet's own mail server lives on the edge, which is also one of the things monitored;
  an outage of the edge would take out the alert about the edge. SES is outside the fleet.
- **The repository is public.** Recipient addresses must stay in the operator's private workspace, never in a template or a test.

## Decision

Three phases, in this order. Each is its own change and none touches a tenant.

### Phase 1: Alertmanager, with email through the operator's SES

- **A fourth app in the `monitor` stack**: Alertmanager, image pinned like the others, on the `mksrv-monitor` network, port 9093
  reachable only where 9090 and 3000 already are (`tailnet_ports`). Its state (silences, notification log) lives on a small
  podman volume; losing it on a restart costs the silences, not the alerts.
- **Prometheus learns where it is** (`alerting:` in `prometheus.yml.tmpl`). The monitor post-deploy hook must reload Alertmanager as well as Prometheus and blackbox (each reads its configuration at start and has its own `/-/reload`): the blackbox exporter ran a September configuration through a deploy for exactly this reason.
- **Receiver: email through the operator's SES SMTP** (`email-smtp.<region>.amazonaws.com:587`, STARTTLS), authenticating with the
  credential that already exists. The password reaches Alertmanager as a podman secret read from a file
  (`smtp_auth_password_file`), the same way Grafana's admin password is delivered, never in the rendered config.
- **Recipients are declared in the operator's `deployment.yaml`** (a new `alerting:` block with the list of addresses and an optional
  sender), validated by the schema, and are **not** in the repository. They are **outside the fleet** (the operator's own external
  mailbox, decided 2026-11-24): a mailbox hosted on the fleet's own mail server cannot receive the alert about the edge being down.
- **Routing by the `severity` label the rules already carry:**
  - `critical`: sent at once (a short `group_wait`), repeated every 4 hours until resolved.
  - `warning`: grouped by alert name, a longer `group_wait`, repeated once a day. Warnings are information to read, not to wake for.
    The operator wants a **daily summary** for them (decided 2026-11-24). Alertmanager groups and spaces notifications but does **not**
    batch them at a fixed hour, so what Phase 1 delivers is the closest it can do (a long `group_wait` and `group_interval`, a 24 h
    `repeat_interval`); a true morning digest is Phase 3.
  - An inhibition rule mutes a `warning` that has the same alert name and instance as a firing `critical`, so one outage is one
    message.
- **Messages carry what the rules already say** (`summary`, `description`, instance, tier, component) and, for the first time, a
  link to the page that explains the alert (`docs/how-to/...`) where one exists.
- **Acceptance:** `amtool check-config` passes on the rendered file in the tests; a deliberately fired test alert **arrives in the
  recipient's mailbox**; resolving it sends the resolved message; a `warning` does not repeat within the day. This needs a real
  send, so it is done once by hand after the deploy and recorded in this ADR.

### Phase 2: a dead man's switch, outside the monitor host

A `Watchdog` alert that is always firing, and something **outside `appd`** that notices when it stops arriving:

- Alertmanager routes `Watchdog` to nothing visible; a small timer on the monitor host asks Alertmanager's API whether `Watchdog` is
  active and, only if it is, publishes a heartbeat as a CloudWatch custom metric. Because the heartbeat is conditional on
  Alertmanager holding the alert, it proves the chain end to end: host up, Prometheus evaluating, Alertmanager running.
- A CloudWatch alarm treats **missing data as breaching** and notifies an SNS topic with an email subscription. AWS delivers that
  mail; it depends on neither `appd`, nor the edge, nor our SES credential. Terraform creates the topic, the alarm and the narrow
  `cloudwatch:PutMetricData` permission for the monitor host's role.
- **Acceptance:** stopping the Alertmanager container for longer than the alarm period produces the email; restarting it clears it.

### Phase 3: a channel through `ifer`, and a daily status mail

**`ifer` (answered 2026-11-24, implemented in its working tree and not yet committed or deployed).** It accepted everything asked: an
authenticated `POST /alertmanager?tenant=<name>` that translates Alertmanager's webhook (one message per call, the highest severity of the
group, `critical` to its highest priority, `RESUELTA` for resolved alerts, so `send_resolved: true`), a bearer token on it and on `/notify`
(the API will not start without one), a new tenant `mksrv-criticas` whose only action is Telegram (it leaves directly, not through the
Redis on `appd`), no fallback to another tenant for an unknown one, and a 10-minute deduplication of identical retries. The token lives in
OpenBao (`kv/tenants/bitabit/ifer`, field `api_token`); mksrv delivers it to Alertmanager as a secret file
(`http_config.authorization.credentials_file`). Still open, and the operator's: **which host runs `ifer` and its mesh address** (it should
listen on the tailnet only; if it were published, TLS is required because the token travels in a header), the Telegram bot and chat, and
deploying it. The critical receiver is not added to mksrv until `ifer` is running and a real alert has crossed the whole path; critical
alerts keep going by email as well.

**A daily digest through `julie` is not viable now** (answered 2026-11-24, nothing implemented): its Prometheus connector speaks HTTPS only,
and its transport blocks private and CGNAT ranges, which includes the tailnet's `100.64.0.0/10`, with no exception planned; it turns a
series into windowed statistics with unknown severity, so a backup component at 0 would not stand out; it detects no alert transitions;
and it has no scheduled digest generation or outbound delivery. Getting there would take three separate deliveries on its side plus a public
HTTPS gateway in front of Prometheus, which this ADR does not want. `julie` remains an option for later and agrees that it cannot replace
Alertmanager.

**What is built instead: a daily status mail from the monitor host.** A systemd timer on `appd` runs once each morning, reads Prometheus
on `localhost`, and sends one email through the same SES credential: the coverage of each snapshot tier, the age of the last backup and
whether each component succeeded, how many alerts are firing, and the `warning`s of the last day. A quiet day is a short message that says
so and states what it could read, never an empty one (which would be indistinguishable from "could not read"). It also works as a second
heartbeat a person notices when it stops arriving. It adds no new service and no network path, and it is the fixed-hour summary that
Alertmanager's own grouping cannot give.

## Alternatives considered

- **Grafana unified alerting.** It is already in the stack and has contact points. But the rules live as code in Prometheus's rule
  file, tested by the render tests, and copying them into Grafana would make two places to keep in step. Grafana can still show
  Alertmanager's alerts.
- **Send through the fleet's own mail server.** Rejected as the primary path for the reason above: the sender would be one of the
  things being reported on.
- **A paged SaaS (an incident-management service).** More than a single-operator fleet needs today, and a new account and
  dependency. Phase 3 leaves the door open.
- **An external heartbeat service (a hosted "dead man's switch").** It works, but it adds a third-party account and sends a
  URL out of the fleet; CloudWatch and SNS are already in the account and in the same billing and permission model.
- **A second Alertmanager or Prometheus for high availability.** Two instances double the operations for a fleet with one operator;
  the dead man's switch covers the real risk, which is silent failure, not a brief outage.
- **Leave it as it is.** The last three days are the argument against.

## Consequences

- **Alert fatigue is a risk to manage, not to ignore.** Once messages go out, any noisy rule becomes mail. The 17 rules are
  reviewed against the severity mapping in Phase 1, and a rule that cannot be acted on is lowered or removed rather than muted in
  Alertmanager.
- **One more app on `appd`** (small: it stores little) and one more secret to deliver. A change to the receiver's address is a
  `deploy` of `monitor`, not a code change.
- **The SES credential is now used by a second consumer** (Keycloak, the relay and Alertmanager). Its rotation (ADR 0025) must
  reach Alertmanager too; the podman secret is replaced on every deploy, so a `deploy` of `monitor` after a rotation is enough.
- **A heartbeat costs a little**, a custom metric and an alarm; the order of magnitude is cents to a few dollars a month, **not
  measured and not checked against the current price list**.
- **`Watchdog` must never be silenced in Alertmanager** and its absence is the signal; this is documented where it is defined.

## Not verified

Everything above is design. Specifically: that the Alertmanager image and version chosen exist and support `smtp_auth_password_file`
(checked at implementation, not now); that `appd` can reach SES on 587 (it reaches the internet through the edge's NAT, and the
edge's own relay already uses that port, but `appd` has not been tried); that mail from `noreply@<root_domain>` to the chosen external
addresses is delivered rather than filtered; that SNS email subscriptions need a one-time confirmation click by the recipient; and
the CloudWatch cost. The operator decided on 2026-11-24 who receives the email, that warnings are a daily summary, and that `ifer` is the channel. What is still
open is `ifer`'s address, its deployment and the Telegram bot and chat (the operator's), that Alertmanager's `credentials_file` and
`send_resolved` behave as `ifer` expects, and the SES delivery to the chosen address.
