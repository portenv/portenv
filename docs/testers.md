# Notes for design testers

What to know before you try Portenv's design-tester build. It's short on purpose; ask if something here doesn't match what you see.

## What you need

- A Mac with Apple silicon and macOS 26.
- **Docker Desktop**, installed and open. Portenv runs your workspaces in it for now. If it isn't running, Portenv says so and waits for it.

## The terminal

Control-click doesn't open the terminal's menu (Copy, Paste, Select All, Find…), because the terminal takes it; right-click or tap with two fingers instead.

## Containers inside a workspace

Docker isn't available inside a workspace yet. Use Docker Desktop on your Mac for containers.

You can install a container engine in a workspace, but it won't start there today: a workspace doesn't get the privileges an engine needs. Portenv never connects a workspace to your Mac's own Docker Desktop, because that would give the workspace control of your Mac.
