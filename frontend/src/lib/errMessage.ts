// Narrow an unknown caught value to a display string, so `catch` blocks don't
// need `catch (err: any)` (which trips @typescript-eslint/no-explicit-any) just
// to read `.message`.
export function errMessage(err: unknown, fallback = 'Something went wrong'): string {
  if (err instanceof Error) return err.message;
  if (typeof err === 'string') return err;
  return fallback;
}
