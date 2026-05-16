local claims = {} + std.extVar('claims');

local email = if 'email' in claims && claims.email != null then claims.email
              else if 'preferred_username' in claims then claims.preferred_username
              else '';

{
  identity: {
    traits: {
      [if email != '' then 'email' else null]: email,
      name: {
        first: if 'given_name' in claims then claims.given_name else '',
        last: if 'family_name' in claims then claims.family_name else '',
      },
      groups: [],
    },
  },
}
