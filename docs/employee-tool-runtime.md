# Current-turn employee tool routing

The authenticated `runtime` may provide `employee_id` and
`employee_capability_origin` beside `employee_command_capability_token`. Portal
owns the default employee, origin and capability grant; the bridge only carries
them into the protected current-turn tool environment as `EMPLOYEE_ID` and
`EMPLOYEE_CAPABILITY_ORIGIN`. They are not extracted from model/user text or
copied into the prompt. An empty employee ID clears a previous turn's default;
the tool must then receive an explicit target, which does not expand authority.
New turns, resumed turns and `respond_interaction.runtime` refreshes replace
these values with the current snapshot. A turn without an employee token does
not export employee routing. The protected-file readers must understand these
two keys before deploying this bridge version; unknown keys still fail closed.

