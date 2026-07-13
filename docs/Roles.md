# Roles and Permissions

Forge uses a role-based access control (RBAC) system to manage permissions. Every API token or user session is assigned a role that determines which actions they can perform.

## Role Hierarchy

The roles are hierarchical, meaning a higher-level role inherits all permissions of the roles below it.

`admin` (100) > `operator` (50) > `viewer` (10) > `agent` (0)

---

## Role Descriptions

### Admin
The highest privilege level. Admins have full control over the entire Forge instance.
- **Use case**: System administrators, infrastructure owners.
- **Key capabilities**: Manage organizations, delete projects, prune historical data, and manage API tokens for other users.

### Operator
Operators can manage the day-to-day lifecycle of projects and pipelines but cannot perform destructive system-wide actions.
- **Use case**: Team leads, DevOps engineers, CI/CD maintainers.
- **Key capabilities**: Create and update projects, manage secrets, define policies, manually trigger runs, approve manual jobs, and cancel active runs.

### Viewer
The default role for most users. Viewers have read-only access to the Forge dashboard and API.
- **Use case**: Developers, stakeholders, auditors.
- **Key capabilities**: View pipeline runs, inspect job logs, browse artifacts, and list projects/policies. They cannot modify any state.

### Agent
A specialized role used by Forge Agents.
- **Use case**: Automated runners (agents) connected to the scheduler.
- **Key capabilities**: Poll for new jobs (lease), report heartbeats, and submit job results/logs.

---

## Permissions Matrix

| Action                                 | Viewer | Operator | Admin |
|:---------------------------------------|:------:|:--------:|:-----:|
| **Visibility**                         |        |          |       |
| View runs, jobs, and log streams       |   ✅    |    ✅     |   ✅   |
| View artifacts                         |   ✅    |    ✅     |   ✅   |
| List projects, orgs, and policies      |   ✅    |    ✅     |   ✅   |
| List secret names (values hidden)      |   ✅    |    ✅     |   ✅   |
| View agent status and health           |   ✅    |    ✅     |   ✅   |
| **Operations**                         |        |          |       |
| Manual trigger (branch/commit)         |   ❌    |    ✅     |   ✅   |
| Approve pending manual jobs            |   ❌    |    ✅     |   ✅   |
| Cancel active runs                     |   ❌    |    ✅     |   ✅   |
| Manage secrets (Set/Delete)            |   ❌    |    ✅     |   ✅   |
| Manage policies (Create/Update/Delete) |   ❌    |    ✅     |   ✅   |
| Create/Update projects                 |   ❌    |    ✅     |   ✅   |
| **Administration**                     |        |          |       |
| Rerun successful/failed runs           |   ❌    |    ❌     |   ✅   |
| Create new organizations               |   ❌    |    ❌     |   ✅   |
| Delete projects                        |   ❌    |    ❌     |   ✅   |
| Manage API tokens (Create/Revoke)      |   ❌    |    ❌     |   ✅   |
| Prune historical runs and logs         |   ❌    |    ❌     |   ✅   |

---

## Assigning Roles

Roles are assigned when creating an API token.

### via CLI
```bash
# Create an operator token
forge token create --name "my-team-lead" --role "operator"
```

### via Environment (Bootstrap)
When Forge starts for the first time, it creates a `root` admin token. You can also pre-seed an agent token:
- `FORGE_ROOT_TOKEN`: Sets the initial admin token.
- `FORGE_API_TOKEN`: If set on the agent, the scheduler can be configured to recognize it.
