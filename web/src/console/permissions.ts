import { computed } from "vue";
import { currentUser, isAuthenticated } from "../api";
export const isAdmin = computed(
  () => isAuthenticated.value && currentUser.value.role === "admin",
);
export const canOperate = computed(
  () =>
    isAuthenticated.value &&
    ["admin", "operator"].includes(currentUser.value.role),
);
