# Packages the PRE-BUILT static agent binary (make agent-linux-<arch>) — no
# compilation happens inside rpmbuild, so the build works offline anywhere
# rpmbuild runs. Invoked by `make rpm`, which passes:
#   --define "lh_version <sanitized git describe>"
#   --define "lh_release <n>"
#   --define "_sourcedir  <staged sources>"
#   --target x86_64|aarch64

%global debug_package %{nil}
%global _build_id_links none

Name:           lighthouse-agent
Version:        %{lh_version}
Release:        %{lh_release}%{?dist}
Summary:        Lighthouse site connectivity agent
License:        Apache-2.0
URL:            https://github.com/devalexllc/lighthouse

Source0:        lighthouse-agent
Source1:        lighthouse-agent.service
Source2:        agent.yaml
Source3:        LICENSE
Source4:        NOTICE
Source5:        THIRD-PARTY-NOTICES

BuildRequires:  systemd-rpm-macros
Requires(pre):  shadow-utils
%{?systemd_requires}

%description
Single static Go binary measuring inter-site connectivity (latency, loss,
jitter, TCP/TLS timings, traceroute) against a Lighthouse control plane
over mTLS. No runtime dependencies. After install: write
/etc/lighthouse/agent.yaml, enroll as the service user, then
`systemctl enable --now lighthouse-agent`.

%prep
# Nothing to do — pre-built binary.

%install
install -D -m 0755 %{SOURCE0} %{buildroot}%{_bindir}/lighthouse-agent
install -D -m 0644 %{SOURCE1} %{buildroot}%{_unitdir}/lighthouse-agent.service
install -D -m 0640 %{SOURCE2} %{buildroot}%{_sysconfdir}/lighthouse/agent.yaml
install -D -m 0644 %{SOURCE3} %{buildroot}%{_defaultlicensedir}/%{name}/LICENSE
install -D -m 0644 %{SOURCE4} %{buildroot}%{_defaultlicensedir}/%{name}/NOTICE
install -D -m 0644 %{SOURCE5} %{buildroot}%{_defaultlicensedir}/%{name}/THIRD-PARTY-NOTICES

%pre
getent group lighthouse >/dev/null || groupadd -r lighthouse
getent passwd lighthouse >/dev/null || \
    useradd -r -g lighthouse -d /var/lib/lighthouse-agent -s /sbin/nologin \
        -c "Lighthouse agent" lighthouse
exit 0

%post
%systemd_post lighthouse-agent.service

%preun
%systemd_preun lighthouse-agent.service

%postun
%systemd_postun_with_restart lighthouse-agent.service

%files
%license %{_defaultlicensedir}/%{name}/LICENSE
%license %{_defaultlicensedir}/%{name}/NOTICE
%license %{_defaultlicensedir}/%{name}/THIRD-PARTY-NOTICES
%{_bindir}/lighthouse-agent
%{_unitdir}/lighthouse-agent.service
%dir %attr(0750,root,lighthouse) %{_sysconfdir}/lighthouse
%config(noreplace) %attr(0640,root,lighthouse) %{_sysconfdir}/lighthouse/agent.yaml

%changelog
# Version history lives in git (Conventional Commits); the RPM is rebuilt
# from source at release time.
