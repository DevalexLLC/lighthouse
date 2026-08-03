# lighthouse-agent systemd unit

The RPM (`packaging/rpm/`) installs the binary, this unit, the service user,
and `/etc/lighthouse/agent.yaml` — prefer it on RHEL-family hosts. Manual
install:

```sh
install -m 0755 lighthouse-agent /usr/bin/lighthouse-agent
install -m 0644 packaging/systemd/lighthouse-agent.service /etc/systemd/system/
useradd --system --home-dir /var/lib/lighthouse-agent --shell /usr/sbin/nologin lighthouse
install -d -m 0750 -g lighthouse /etc/lighthouse
# write /etc/lighthouse/agent.yaml (see packaging/rpm/agent.yaml), then
# enroll AS THE SERVICE USER so the pki files land with the right owner:
#   install -d -m 0700 -o lighthouse -g lighthouse /var/lib/lighthouse-agent
#   sudo -u lighthouse lighthouse-agent enroll --config /etc/lighthouse/agent.yaml \
#     --token <join-token> (--ca-cert <file> | --fingerprint sha256:<hex>)
systemctl daemon-reload
systemctl enable --now lighthouse-agent
```

The unit runs `selfcheck` as ExecStartPre: a misconfigured install refuses to
start and journalctl shows every failing check with its remedy. Enrollment
runs outside the unit (an operator command), so the sandbox does not apply
to it — but the service only reads what enrollment wrote, so ownership must
match the service user (hence `sudo -u lighthouse` above).

Run manual `selfcheck` invocations as the service user too
(`sudo -u lighthouse lighthouse-agent selfcheck …`): the spool check
creates `<state_dir>/spool` if missing, and a root-owned spool directory
would break the service's first start.

## Sandbox

`ProtectSystem=strict` plus `StateDirectory` means the only writable path is
`/var/lib/lighthouse-agent` — exactly the spool + pki contract. The unit
comments document why each address family stays open. Certificate renewal
happens in-process over the existing mTLS channel and writes only inside the
state directory, so it works under the full sandbox.

Verify on a RHEL 9 host after install:

```sh
systemd-analyze security lighthouse-agent   # expect a low exposure score
journalctl -u lighthouse-agent -b           # selfcheck lines all OK
```

## ICMP privileges

The unit grants `CAP_NET_RAW`, which covers both the ICMP echo prober's raw
fallback and the traceroute prober (which strictly requires a raw ICMP
socket — unprivileged datagram sockets do not deliver time-exceeded errors
for UDP probes).

The echo prober alone can run without any capability via unprivileged
datagram ICMP if the kernel allows the service group:

```sh
# gid of the lighthouse group, e.g. 987
sysctl -w net.ipv4.ping_group_range="987 987"
```

`lighthouse-agent selfcheck` reports which modes are available.
