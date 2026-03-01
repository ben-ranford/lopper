defmodule Fixture do
  alias Jason
  alias NimbleCSV.RFC4180, as: CSV

  def parse(value) do
    Jason.decode!(value)
    CSV.parse_string("a,b\n")
  end
end
