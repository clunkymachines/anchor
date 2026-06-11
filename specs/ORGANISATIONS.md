# Organisations

## Purpose

Organisations are Anchor's collaboration and tenancy boundary. Devices, software releases, OTA deployments, members, roles, and organisation settings belong to one organisation and must stay isolated from other organisations.

Each user gets a personal organisation when their account is created. This default organisation acts as a private sandbox where the user can try Anchor without inviting anyone else.

Users can also create additional organisations for shared work. Shared organisations behave like team spaces: the creator becomes an organisation admin, invites other users, and manages their access.

## Core Model

A user may belong to multiple organisations. A user may create organisations beyond their personal sandbox.

Organisation names are globally unique.

The user who creates an organisation is automatically an admin of that organisation.

Organisation admin is scoped to one organisation. It is separate from the global application admin flag, which grants broader operational access across Anchor.

An organisation cannot have no admin. Any action that would remove, demote, or deactivate the final admin must be rejected.

## Membership And Invitations

Organisation admins can invite users to join an organisation.

If the invited email already belongs to an Anchor user, that user is added directly to the organisation.

If the invited email does not belong to an Anchor user, Anchor generates an invitation URL. The URL contains a unique random token that expires. The invited person can use that URL to create an account and join the organisation.

Invitation URLs are shown to the admin who created the invite because Anchor does not send email yet.

Membership is organisation-scoped. Being invited to one organisation does not grant access to any other organisation owned by the same creator or by other users.

Only organisation admins can remove other users from an organisation.

## RBAC

Access control is based on organisation-scoped roles and permissions.

Organisation admins can create roles, attach permissions to those roles, and assign roles to organisation members.

The admin capability is protected. It must not be possible to leave an organisation without at least one admin.

The complete permission catalog can be defined during implementation. Expected permission areas include:

- managing devices
- managing software releases
- managing OTA deployments
- viewing telemetry and device events
- managing members and invitations
- managing roles and permissions
- changing organisation settings

Role changes only affect the selected organisation.

## Organisation Management Screen

Anchor should have a dedicated organisation management screen with an entry in the main menu.

The screen should allow organisation admins to:

- change the organisation name
- invite users
- see the member list
- see the admin list
- remove members
- create roles
- assign permissions to roles
- assign roles to users

Only users with organisation admin rights can perform management actions. Non-admin members should not see or reach controls that mutate organisation membership, roles, permissions, or settings.

When an admin invites an email address that does not belong to an existing user, the screen should show the generated invitation URL so the admin can share it manually.

## Invariants

Every user has a personal organisation created at account creation.

Organisation names are globally unique.

Every organisation has at least one admin.

The organisation creator starts as an admin.

Organisation data remains isolated from other organisations.

Organisation membership, roles, permissions, and admin status are scoped to one organisation.

The selected organisation controls which devices, releases, deployments, and telemetry a user can access.

## Future Acceptance Notes

Creating a user creates a personal organisation and adds the user as admin.

Creating an organisation adds the creator as admin.

Removing or demoting the final admin is rejected.

Only organisation admins can invite users, remove other users, edit roles, assign roles, view the admin list, and rename the organisation.

Inviting an existing user adds that user directly to the organisation.

Inviting a non-existing user creates an expiring invitation token and returns an invitation URL.

Using a valid invitation URL lets the invited user create an account and join the organisation.

Non-admin members cannot access organisation management actions.

Organisation selectors and resource pages only expose organisations the current user belongs to, except for global application admins.

## Open Questions

The exact permission names and grouping are still open.

The invitation token expiration duration and resend flow are still open.

Whether users can leave or delete their personal organisation is still open.
