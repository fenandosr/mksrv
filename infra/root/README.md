# Terraform root

The CLI runs this root from the versioned engine cache while keeping the
Terraform data directory, generated provider/backend files, variables, and state
inside the private workspace. M0 contains only a validation-safe contract;
resource wiring begins in M1.
