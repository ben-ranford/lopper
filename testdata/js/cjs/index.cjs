const { debounce } = require("lodash")
const emit = debounce(console.log, 10)
emit("first")
emit("last")
