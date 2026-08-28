# Guía rápida

`mksrv` declara y opera *stacks* de servicios self-hosted sobre AWS y hosts
Rocky Linux existentes, cableados por una malla Headscale.

## Compilar

```bash
make build
./bin/mksrv version
```

Requiere Go 1.25 o superior.

## Crear un workspace

El *workspace* es privado y contiene los datos reales del despliegue. **No** debe
vivir dentro del repositorio del engine.

```bash
mksrv init ~/deploys/prod.workspace \
  --region us-east-1 --root-domain example.com \
  --mgmt-cidr 203.0.113.4/32 --acme-email ops@example.com
```

Edita `deployment.yaml` (zona Route 53, hosts) y agrega `tenants/<id>.yaml`.

```bash
mksrv validate
mksrv doctor
```

## Provisionar

```bash
# Infraestructura (VPC, EC2, EIP, security groups, DNS de operador).
mksrv apply --infra-only --trust-hosts

# Bootstrap de los hosts + stack base + resto de stacks.
mksrv apply --trust-hosts

# Malla Headscale (usuarios por tenant, unión de los hosts).
mksrv mesh

# Realms Keycloak, clientes OIDC, configd, bases de datos por tenant.
mksrv tenant apply
mksrv users apply
```

## Operar

```bash
mksrv status                 # salud de la flota
mksrv deploy [HOST] [--stack N]   # re-desplegar tras cambios
mksrv unlock --infra-only LOCK_ID # liberar un lock de estado colgado
mksrv destroy --infra-only        # teardown (deja el backend de estado)
```

Toda salida legible va a stderr; el JSON de `--json` va a stdout.

## Contrato del workspace

```
deployment.yaml           entorno, AWS, backend, DNS, identidad y hosts
tenants/<id>.yaml          una empresa cliente; los FQDN derivan de base_domain
tenants/<id>.users.yaml    lista declarativa de usuarios (opcional)
secrets.sops.yaml          cifrado con sops+age (opcional hasta usar secretos)
mksrv.lock                 versión del engine del último apply exitoso
.mksrv/                    estado generado (tfvars, plan, outputs, mesh.json)
```

Ver `docs/architecture.md`, `docs/workspace.md` y `docs/stacks.md`.
