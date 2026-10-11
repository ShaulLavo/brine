import { ConfirmedSession } from './session';
import { DbConnection } from './module_bindings';

declare const unconfirmed: DbConnection;
// @ts-expect-error an unconfirmed raw SDK connection is not a confirmed recovery session
const invalid: ConfirmedSession = unconfirmed;
// @ts-expect-error only the explicit confirmed connection factory may create a recovery session
new ConfirmedSession(unconfirmed);
void invalid;
