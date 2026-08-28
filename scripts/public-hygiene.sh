#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

python3 - <<'PY'
from __future__ import annotations

import ipaddress
import pathlib
import re
import sys

root = pathlib.Path.cwd()
excluded_parts = {'.git', 'bin', 'dist', '.terraform', '__pycache__'}
text_suffixes = {
    '.go', '.md', '.yaml', '.yml', '.json', '.tf', '.tmpl', '.sh', '.toml',
    '.mod', '.sum', '.txt', '.gitignore'
}
explicit_text_names = {'Makefile', 'LICENSE', '.gitignore', '.goreleaser.yaml', '.gitleaks.toml', '.pre-commit-config.yaml'}

findings: list[str] = []

aws_account = re.compile(r'(?<![A-Za-z0-9])[0-9]{12}(?![A-Za-z0-9])')
# Well-known public vendor AWS account ids that legitimately appear in AMI
# owner filters. These are not operator identifiers.
public_vendor_accounts = {
    '792107900819',  # Rocky Linux (official AMI publisher)
}
aws_key = re.compile(r'\b(?:AKIA|ASIA)[A-Z0-9]{16}\b')
# Only flag ARNs that embed a literal 12-digit account id. Service-owned
# (arn:aws:iam::aws:...) and wildcard/templated ARNs in Terraform policy
# documents are fine.
arn = re.compile(r'\barn:(?:aws|aws-cn|aws-us-gov):[a-z0-9-]*:[a-z0-9-]*:[0-9]{12}:')
private_key = re.compile(r'-----BEGIN (?:RSA |EC |OPENSSH |DSA )?PRIVATE KEY-----')
email = re.compile(r'(?<![A-Za-z0-9._%+-])([A-Za-z0-9._%+-]+)@([A-Za-z0-9.-]+\.[A-Za-z]{2,})(?![A-Za-z0-9.-])')
ipv4 = re.compile(r'(?<![0-9.])(?:[0-9]{1,3}\.){3}[0-9]{1,3}(?![0-9.])')
domain_assignment = re.compile(
    r'(?im)^\s*(?:root_domain|base_domain|keycloak_domain|headscale_domain|domain|fqdn|hostname)\s*[:=]\s*["\']?([A-Za-z0-9.-]+\.[A-Za-z]{2,})'
)

allowed_domain_suffixes = (
    'example.com', 'example.org', '.invalid', 'localhost',
)
allowed_documentation_nets = (
    ipaddress.ip_network('192.0.2.0/24'),
    ipaddress.ip_network('198.51.100.0/24'),
    ipaddress.ip_network('203.0.113.0/24'),
)

for path in sorted(root.rglob('*')):
    if not path.is_file() or any(part in excluded_parts for part in path.relative_to(root).parts):
        continue
    if path.name not in explicit_text_names and path.suffix not in text_suffixes:
        continue
    try:
        text = path.read_text(encoding='utf-8')
    except UnicodeDecodeError:
        continue
    rel = path.relative_to(root).as_posix()

    if any(match.group(0) not in public_vendor_accounts for match in aws_account.finditer(text)):
        findings.append(f'{rel}: possible AWS account ID')
    if aws_key.search(text):
        findings.append(f'{rel}: possible AWS access key ID')
    if arn.search(text):
        findings.append(f'{rel}: AWS ARN is forbidden in the public repository')
    if private_key.search(text):
        findings.append(f'{rel}: private key material detected')

    for match in email.finditer(text):
        domain = match.group(2).lower().rstrip('.')
        if not any(domain == suffix or domain.endswith('.' + suffix) for suffix in ('example.com', 'example.org')) and not domain.endswith('.invalid'):
            findings.append(f'{rel}: non-synthetic email domain {domain!r}')

    for match in ipv4.finditer(text):
        try:
            ip = ipaddress.ip_address(match.group(0))
        except ValueError:
            continue
        allowed = (
            ip.is_private or ip.is_loopback or ip.is_link_local or ip.is_unspecified
            or any(ip in network for network in allowed_documentation_nets)
        )
        if not allowed:
            findings.append(f'{rel}: public IPv4 address {ip} is not a reserved example')

    if path.suffix in {'.yaml', '.yml', '.json', '.toml', '.tf', '.md'}:
        for match in domain_assignment.finditer(text):
            domain = match.group(1).lower().rstrip('.')
            # Skip HCL expression references like local.d.dns.root_domain.
            if domain.split('.', 1)[0] in {'local', 'var', 'module', 'data', 'each', 'self', 'path'}:
                continue
            if not any(domain == suffix or domain.endswith('.' + suffix) for suffix in allowed_domain_suffixes):
                findings.append(f'{rel}: configured domain {domain!r} is not synthetic')

for go_file in sorted(root.rglob('*.go')):
    if any(part in excluded_parts for part in go_file.relative_to(root).parts):
        continue
    first_nonempty = next((line.strip() for line in go_file.read_text(encoding='utf-8').splitlines() if line.strip()), '')
    if first_nonempty != '// SPDX-License-Identifier: Apache-2.0':
        findings.append(f'{go_file.relative_to(root).as_posix()}: missing SPDX header')

for forbidden in ('deployment.yaml', 'secrets.sops.yaml'):
    candidate = root / forbidden
    if candidate.exists():
        findings.append(f'{forbidden}: private workspace file exists at repository root')
if (root / 'tenants').exists():
    findings.append('tenants/: private workspace directory exists at repository root')

if findings:
    print('public-repository hygiene failed:', file=sys.stderr)
    for finding in findings:
        print(f'  - {finding}', file=sys.stderr)
    raise SystemExit(1)

print('public-repository hygiene passed')
PY
