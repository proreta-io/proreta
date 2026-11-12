import { useAuth } from "../auth/AuthProvider";
import { abilities, type Abilities } from "./roles";
import { useProjectScope } from "./useProjectScope";

/** The abilities of the session, with the roles it holds in its projects. */
export function useAbilities(): Abilities {
  const session = useAuth().session;
  const { projects } = useProjectScope();
  return abilities(session, projects.data?.items);
}
