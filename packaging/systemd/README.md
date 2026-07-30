# lighthouse-agent systemd unit

Install (until the M6 RPM does this):

```sh
install -m 0755 lighthouse-agent /usr/bin/lighthouse-agent
install -m 0644 packaging/systemd/lighthouse-agent.service /etc/systemd/system/
useradd --system --home-dir /var/lib/lighthouse-agent --shell /usr/sbin/nologin lighthouse
install -d -m 0750 /etc/lighthouse
# write /etc/lighthouse/agent.yaml, then enroll:
#   lighthouse-agent enroll --config /etc/lighthouse/agent.yaml --token <join-token> \
#     (--ca-cert <file> | --fingerprint sha256:<hex>)
systemctl daemon-reload
systemctl enable --now lighthouse-agent
```

The unit runs `selfcheck` as ExecStartPre: a misconfigured install refuses to
start and journalctl shows every failing check with its remedy.

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
