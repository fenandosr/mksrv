# Open operator questions

No answer is required for M0. Defaults allow implementation to proceed through
interfaces and synthetic tests.

1. Which organization/user replaces the module owner placeholder for the first
   public release?
2. Will the first real environment use one AWS account per environment or shared
   accounts with naming/tag boundaries? Default: shared accounts are acceptable
   when isolated by state, IAM path, tags, and names.
3. Which DNS provider is first for real acceptance testing? Default: Route 53;
   manual remains available.
4. Should the first mail deployment be SES send-only? Default: yes; inbound is
   disabled.
5. What host-key trust policy should production SSH use? Default proposal for M2:
   explicit known-hosts file in the workspace, with first-use enrollment only
   through an interactive command.
6. Which Terraform version will M1 pin after compatibility testing? Default: the
   newest tested minor that supports all selected providers; never an unbounded
   `latest` lookup during apply.
