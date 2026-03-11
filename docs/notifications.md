# Notifications

Lopper can send analysis summaries to Slack and Microsoft Teams via incoming webhooks.

## CLI flags

```bash
lopper analyse --top 20 \
  --repo . \
  --language all \
  --notify-slack "$LOPPER_NOTIFY_SLACK_WEBHOOK" \
  --notify-teams "$LOPPER_NOTIFY_TEAMS_WEBHOOK" \
  --notify-on breach
```

Available flags:

- `--notify-slack URL`: Slack webhook endpoint.
- `--notify-teams URL`: Teams webhook endpoint.
- `--notify-on MODE`: Trigger mode for both channels (`always|breach|regression|improvement`).

## Trigger behavior

- `always`: send notification on every run.
- `breach`: send only when threshold gating is breached.
- `regression`: send only when baseline comparison shows positive waste increase.
- `improvement`: send only when baseline comparison shows negative waste increase.

## Config file

`.lopper.yml`:

```yaml
notifications:
  on: breach
  slack:
    webhook: https://hooks.slack.com/services/T000/B000/SECRET
  teams:
    webhook: https://outlook.office.com/webhook/SECRET
    on: improvement
```

`lopper.json`:

```json
{
  "notifications": {
    "on": "breach",
    "slack": {
      "webhook": "https://hooks.slack.com/services/T000/B000/SECRET"
    },
    "teams": {
      "webhook": "https://outlook.office.com/webhook/SECRET",
      "on": "improvement"
    }
  }
}
```

## Environment variables

- `LOPPER_NOTIFY_ON`
- `LOPPER_NOTIFY_SLACK_WEBHOOK`
- `LOPPER_NOTIFY_TEAMS_WEBHOOK`

## Precedence

Notification configuration is resolved in this order:

`CLI > env > config > defaults`

## Payload format

- Slack receives a Block Kit payload (`text` fallback + `blocks`).
- Teams receives a Microsoft Adaptive Card envelope (`application/vnd.microsoft.card.adaptive`).
