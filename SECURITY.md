# Security policy

Do not open public issues containing deployment files, Terraform state, SOPS
content, API tokens, SSH material, cloud identifiers, or production hostnames.
Use the repository security-advisory channel after the project owner enables it.

M0 performs only local file and environment inspection. It makes no hidden
network calls. Later milestones must document every outbound integration,
redact secrets, and keep secret values out of Terraform state and logs.
