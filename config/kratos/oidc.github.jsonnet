local claims = {
  email_verified: false,
} + std.extVar('claims');

{
  identity: {
    traits: {
      [if 'email' in claims && claims.email != null then 'email' else null]: claims.email,
      name: {
        first: if 'name' in claims && claims.name != null then claims.name else if 'login' in claims then claims.login else '',
        last: '',
      },
    },
  },
}
