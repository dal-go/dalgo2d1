package dalgo2d1

// NorthwindSchema returns an independent allowlist for the pinned Northwind
// sample's 13 tables and 17 views. Names and field order follow its published
// SQLite contract; views have no primary key.
func NorthwindSchema() Schema {
	return Schema{
		"Categories":                     {Columns: []string{"CategoryID", "CategoryName", "Description", "Picture"}, PrimaryKey: []string{"CategoryID"}, BlobColumns: []string{"Picture"}},
		"CustomerCustomerDemo":           {Columns: []string{"CustomerID", "CustomerTypeID"}, PrimaryKey: []string{"CustomerID", "CustomerTypeID"}},
		"CustomerDemographics":           {Columns: []string{"CustomerTypeID", "CustomerDesc"}, PrimaryKey: []string{"CustomerTypeID"}},
		"Customers":                      {Columns: []string{"CustomerID", "CompanyName", "ContactName", "ContactTitle", "Address", "City", "Region", "PostalCode", "Country", "Phone", "Fax"}, PrimaryKey: []string{"CustomerID"}},
		"EmployeeTerritories":            {Columns: []string{"EmployeeID", "TerritoryID"}, PrimaryKey: []string{"EmployeeID", "TerritoryID"}},
		"Employees":                      {Columns: []string{"EmployeeID", "LastName", "FirstName", "Title", "TitleOfCourtesy", "BirthDate", "HireDate", "Address", "City", "Region", "PostalCode", "Country", "HomePhone", "Extension", "Photo", "Notes", "ReportsTo", "PhotoPath"}, PrimaryKey: []string{"EmployeeID"}, BlobColumns: []string{"Photo"}},
		"Order Details":                  {Columns: []string{"OrderID", "ProductID", "UnitPrice", "Quantity", "Discount"}, PrimaryKey: []string{"OrderID", "ProductID"}},
		"Orders":                         {Columns: []string{"OrderID", "CustomerID", "EmployeeID", "OrderDate", "RequiredDate", "ShippedDate", "ShipVia", "Freight", "ShipName", "ShipAddress", "ShipCity", "ShipRegion", "ShipPostalCode", "ShipCountry"}, PrimaryKey: []string{"OrderID"}},
		"Products":                       {Columns: []string{"ProductID", "ProductName", "SupplierID", "CategoryID", "QuantityPerUnit", "UnitPrice", "UnitsInStock", "UnitsOnOrder", "ReorderLevel", "Discontinued"}, PrimaryKey: []string{"ProductID"}},
		"Regions":                        {Columns: []string{"RegionID", "RegionDescription"}, PrimaryKey: []string{"RegionID"}},
		"Shippers":                       {Columns: []string{"ShipperID", "CompanyName", "Phone"}, PrimaryKey: []string{"ShipperID"}},
		"Suppliers":                      {Columns: []string{"SupplierID", "CompanyName", "ContactName", "ContactTitle", "Address", "City", "Region", "PostalCode", "Country", "Phone", "Fax", "HomePage"}, PrimaryKey: []string{"SupplierID"}},
		"Territories":                    {Columns: []string{"TerritoryID", "TerritoryDescription", "RegionID"}, PrimaryKey: []string{"TerritoryID"}},
		"Alphabetical list of products":  {Columns: []string{"ProductID", "ProductName", "SupplierID", "CategoryID", "QuantityPerUnit", "UnitPrice", "UnitsInStock", "UnitsOnOrder", "ReorderLevel", "Discontinued", "CategoryName"}},
		"Category Sales for 1997":        {Columns: []string{"CategoryName", "CategorySales"}},
		"Current Product List":           {Columns: []string{"ProductID", "ProductName"}},
		"Customer and Suppliers by City": {Columns: []string{"City", "CompanyName", "ContactName", "Relationship"}},
		"Invoices":                       {Columns: []string{"ShipName", "ShipAddress", "ShipCity", "ShipRegion", "ShipPostalCode", "ShipCountry", "CustomerID", "CustomerName", "Address", "City", "Region", "PostalCode", "Country", "Salesperson", "OrderID", "OrderDate", "RequiredDate", "ShippedDate", "ShipperName", "ProductID", "ProductName", "UnitPrice", "Quantity", "Discount", "ExtendedPrice", "Freight"}},
		"Order Details Extended":         {Columns: []string{"OrderID", "ProductID", "ProductName", "UnitPrice", "Quantity", "Discount", "ExtendedPrice"}},
		"Order Subtotals":                {Columns: []string{"OrderID", "Subtotal"}},
		"Orders Qry":                     {Columns: []string{"OrderID", "CustomerID", "EmployeeID", "OrderDate", "RequiredDate", "ShippedDate", "ShipVia", "Freight", "ShipName", "ShipAddress", "ShipCity", "ShipRegion", "ShipPostalCode", "ShipCountry", "CompanyName", "Address", "City", "Region", "PostalCode", "Country"}},
		"Product Sales for 1997":         {Columns: []string{"CategoryName", "ProductName", "ProductSales"}},
		"ProductDetails_V":               {Columns: []string{"ProductID", "ProductName", "SupplierID", "CategoryID", "QuantityPerUnit", "UnitPrice", "UnitsInStock", "UnitsOnOrder", "ReorderLevel", "Discontinued", "CategoryName", "CategoryDescription", "SupplierName", "SupplierRegion"}},
		"Products Above Average Price":   {Columns: []string{"ProductName", "UnitPrice"}},
		"Products by Category":           {Columns: []string{"CategoryName", "ProductName", "QuantityPerUnit", "UnitsInStock", "Discontinued"}},
		"Quarterly Orders":               {Columns: []string{"CustomerID", "CompanyName", "City", "Country"}},
		"Sales Totals by Amount":         {Columns: []string{"SaleAmount", "OrderID", "CompanyName", "ShippedDate"}},
		"Sales by Category":              {Columns: []string{"CategoryID", "CategoryName", "ProductName", "ProductSales"}},
		"Summary of Sales by Quarter":    {Columns: []string{"ShippedDate", "OrderID", "Subtotal"}},
		"Summary of Sales by Year":       {Columns: []string{"ShippedDate", "OrderID", "Subtotal"}},
	}
}
