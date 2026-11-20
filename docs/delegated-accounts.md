# Delegated panel accounts

All accounts sign in on the existing panel login page. The original panel owner keeps the full panel and its existing authentication settings.

| Role | Access |
| --- | --- |
| Owner | Full panel; creates sudo admins, admins, and resellers; grants new credit; chooses eligible inbounds. |
| Sudo admin | Own clients, direct reports, credit transfers, and a read-only Xray overview without client records or credentials. |
| Admin | Own clients, direct reports, and credit transfers. Can create admins and resellers. |
| Reseller | Own clients and subscription links. |

To start, create or enable a local VLESS, VMess, or Trojan inbound. Open **Admin management**, switch the inbound on for delegated accounts, create an account, add both client slots and traffic credit, and use **Manage inbounds** on that account to assign the inbound. A manager can assign its own available inbounds to direct reports. Revoking an inbound from a manager also suspends access for its descendants. Existing accounts retain access to previously enabled inbounds during migration. Sign out before signing in with the new credentials.

Credit is available in whole GiB and whole client slots. Issuing a client consumes one slot and the full traffic allowance from the creator's balance. Moving credit to a direct report deducts the same amount from the parent. Usage, expiry, or the owner's deletion of a client does not replenish issuance credit.

The owner can still inspect existing and delegated clients in the main panel. Delegated users see only clients they issued and do not have access to global client exports or live event streams. Existing clients are not automatically assigned to new accounts. Delegated accounts use their individual password; the owner's panel-wide authenticator and LDAP configuration are not shared with them.
