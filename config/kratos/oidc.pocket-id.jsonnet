local claims = {
  email_verified: true,
} + std.extVar('claims');

{
  identity: {
    traits: {
      [if 'email' in claims then 'email' else null]: claims.email,
      name: {
        first: if 'given_name' in claims then claims.given_name else '',
        last: if 'family_name' in claims then claims.family_name else '',
      },
      groups: if 'groups' in claims then claims.groups else [],
    },
  },
}
