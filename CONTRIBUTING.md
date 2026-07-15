# Contributing

Anchor is licensed under the GNU Affero General Public License v3.0.

Before Clunky Machines can accept a contribution, the contributor must sign the [Contributor Copyright Assignment Agreement](CLA.md). The agreement assigns copyright in accepted contributions to Clunky Machines and gives the contributor a broad license back to reuse their own contributions.

Do not submit code, documentation, designs, media, or other copyrightable material unless you have the rights needed to submit it under the agreement.

## Logging

Anchor uses Go's `log/slog` package for application logs.

- `Info`: one-time lifecycle messages that do not recur during normal operation, such as `app started`.
- `Debug`: recurring operational messages. Debug logs should stay quiet by default.
- `Warn`: recoverable problems that may need attention but do not require immediate intervention, such as malformed device payloads.
- `Error`: serious problems that require immediate human intervention, such as database corruption, crashes, or application bugs.
