# Security policy

## Reporting a vulnerability

Please do not open a public issue, pull request or discussion for a security
problem. Report it privately through GitHub instead:

1. Open the repository's **Security** tab.
2. Click **Report a vulnerability** and fill in the advisory form.

The report is visible only to the maintainers. Include the affected version
(`daymug --version`), how DayMug is deployed (binary, systemd/launchd service
or Docker), and the steps or proof of concept that reproduce the issue.

We will acknowledge the report, keep you updated while a fix is prepared, and
credit you in the advisory unless you ask us not to.

## Supported versions

Only the latest release receives security fixes. DayMug upgrades itself in
place (`daymug upgrade`, or the admin upgrade panel), so the fix for a
reported issue ships as a new patch release.
